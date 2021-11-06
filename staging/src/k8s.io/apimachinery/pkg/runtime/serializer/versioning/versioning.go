/*
Copyright 2014 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package versioning

import (
	"encoding/json"
	"io"
	"reflect"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

// f.scheme, f.legacySerializer, f.universal, schema.GroupVersions(version), runtime.InternalGroupVersioner
// encoder = f.legacySerializer
// decoder = f.universal
// encodeVersion = schema.GroupVersions(version)
// decodeVersion = runtime.InternalGroupVersioner

// NewDefaultingCodecForScheme is a convenience method for callers that are using a scheme.
func NewDefaultingCodecForScheme(
	// TODO: I should be a scheme interface?
	scheme *runtime.Scheme,
	encoder runtime.Encoder,
	decoder runtime.Decoder,
	encodeVersion runtime.GroupVersioner,
	decodeVersion runtime.GroupVersioner,
) runtime.Codec {
	return NewCodec(encoder, decoder, runtime.UnsafeObjectConvertor(scheme), scheme, scheme, scheme, encodeVersion, decodeVersion, scheme.Name())
}

// NewCodec
// NewCodec takes objects in their internal versions and converts them to external versions before
// serializing them. It assumes the serializer provided to it only deals with external versions.
// This class is also a serializer, but is generally used with a specific version.
func NewCodec(
	encoder runtime.Encoder,
	decoder runtime.Decoder,
	convertor runtime.ObjectConvertor,
	creater runtime.ObjectCreater,
	typer runtime.ObjectTyper,
	defaulter runtime.ObjectDefaulter,
	encodeVersion runtime.GroupVersioner,
	decodeVersion runtime.GroupVersioner,
	originalSchemeName string,
) runtime.Codec {
	internal := &codec{
		encoder:   encoder,
		decoder:   decoder,
		convertor: convertor,
		creater:   creater,
		typer:     typer,
		defaulter: defaulter,

		encodeVersion: encodeVersion,
		decodeVersion: decodeVersion,

		identifier: identifier(encodeVersion, encoder),

		originalSchemeName: originalSchemeName,
	}
	return internal
}

// ok
type codec struct {
	// 编码器（序列化），序列化的格式（json、yaml、pb）已经包含在 encoder 中了
	encoder   runtime.Encoder
	// 解码器（反序列化），解码的目标 gvk 可以从 obj 中获取
	decoder   runtime.Decoder
	// 用于资源的正常版本和内部版本之间的相互转换
	convertor runtime.ObjectConvertor
	// 根据 gvk 信息创建 obj，
	// 用于资源在 decode 操作中正常版本的创建
	creater   runtime.ObjectCreater
	// 根据 obj 确定资源的类型 gvk
	typer     runtime.ObjectTyper
	// 为 obj 填充默认值
	//用于资源在 decode 操作中正常版本的默认值创建
	defaulter runtime.ObjectDefaulter

	// 使用（encodeVersion，decodeVersion） codec 进行 encode，会将某 gvk 版本的 obj 序列化为 gvk 为 encodeVersion 的 obj（序列化格式由 encoder 决定）
	// 使用（encodeVersion，decodeVersion） codec 进行 decoder，能够解析某序列化格式（json、pb、yaml-序列化格式由 decoder 决定）的数据为 gvk 为 decodeVersion 的 obj
	// encode 的时候会把 obj 先转换为 encodeVersion，然后在调用 encoder，序列化为某一格式（json、yaml、pb）
	encodeVersion runtime.GroupVersioner
	// 反序列化时指定的版本，作为默认版本传入
	// TODO：最终 decoder 的结果为 decodeVersion 的 obj——是不是如果 decode 的时候传入了 into，那么使用 into；如果没有传入into，就是用这个版本作为转换的结果？
	decodeVersion runtime.GroupVersioner

	identifier runtime.Identifier

	// originalSchemeName is optional, but when filled in it holds the name of the scheme from which this codec originates
	originalSchemeName string
}

var identifiersMap sync.Map

type codecIdentifier struct {
	EncodeGV string `json:"encodeGV,omitempty"`
	Encoder  string `json:"encoder,omitempty"`
	Name     string `json:"name,omitempty"`
}

// identifier computes Identifier of Encoder based on codec parameters.
func identifier(encodeGV runtime.GroupVersioner, encoder runtime.Encoder) runtime.Identifier {
	result := codecIdentifier{
		Name: "versioning",
	}

	if encodeGV != nil {
		result.EncodeGV = encodeGV.Identifier()
	}
	if encoder != nil {
		result.Encoder = string(encoder.Identifier())
	}
	if id, ok := identifiersMap.Load(result); ok {
		return id.(runtime.Identifier)
	}
	identifier, err := json.Marshal(result)
	if err != nil {
		klog.Fatalf("Failed marshaling identifier for codec: %v", err)
	}
	identifiersMap.Store(result, runtime.Identifier(identifier))
	return runtime.Identifier(identifier)
}

// 目标：将 data 解码为 into；1)如果 into 为空，将其转换为 codec.decodeVersion;2)如果 into 不为空，将data 转换为 into 版本（data 和 into 版本可能相同，可能不同）
// 1、如果 into 为 空：将解码好的 obj 进一步转换为 c.decodeVersion 版本
// 2、into 不为空，并且 decode 时，从数据中获取+后续填充的 gvk 信息与传入的 into obj 的 gvk 信息完全相同：直接返回解码好的 obj
// 3、into 不为空，并且 decode 时，从数据中获取+后续填充的 gvk 信息与传入的 into obj 的 gvk 信息不完全相同：进行 convert——这里涉及到 decodeVersion，但是不知道 decodeVersion 在convert中到作用
//
// 尝试反序列为 obj，然后再将其转换为 c.decodeVersion 版本。
// Decode attempts a decode of the object, then tries to convert it to the internal version. If into is provided and the decoding is
// successful, the returned runtime.Object will be the value passed as into. Note that this may bypass conversion if you pass an
// into that matches the serialized version.
func (c *codec) Decode(data []byte, defaultGVK *schema.GroupVersionKind, into runtime.Object) (runtime.Object, *schema.GroupVersionKind, error) {
	// If the into object is unstructured and expresses an opinion about its group/version,
	// create a new instance of the type so we always exercise the conversion path (skips short-circuiting on `into == obj`)
	// 如果 into 是 unstructured 类型的，并且他的 gv 信息不为空（说明是 crd 资源？），那么创建一个新的 instance
	// TODO：为什么要这么做？
	decodeInto := into
	if into != nil {
		if _, ok := into.(runtime.Unstructured); ok && !into.GetObjectKind().GroupVersionKind().GroupVersion().Empty() {
			decodeInto = reflect.New(reflect.TypeOf(into).Elem()).Interface().(runtime.Object)
		}
	}

	// 反序列化为某个gvk的obj（gvk 信息从 data、defaultGVK、decodeInto 中获取）
	obj, gvk, err := c.decoder.Decode(data, defaultGVK, decodeInto)
	if err != nil {
		return nil, gvk, err
	}

	// Q：这个是什么意思
	// A：如果 obj 有需要嵌套的编解码其成员，需要调用这个函数
	if d, ok := obj.(runtime.NestedObjectDecoder); ok {
		if err := d.DecodeNestedObjects(runtime.WithoutVersionDecoder{c.decoder}); err != nil {
			return nil, gvk, err
		}
	}

	// 如果制定了反序列化的obj
	// if we specify a target, use generic conversion.
	if into != nil {
		// 填充默认值
		// perform defaulting if requested
		if c.defaulter != nil {
			c.defaulter.Default(obj)
		}

		// decode 时，从数据中获取+后续填充的 gvk 信息与传入的 into obj 的 gvk 信息完全相同
		// Short-circuit conversion if the into object is same object
		if into == obj {
			return into, gvk, nil
		}

		// 此时 obj 的 gvk 信息于 into 的 gvk 信息不匹配，所以要尝试进行转换
		// TODO：这里的 decodeVersion 不确定是什么用——是先将 obj 转换为 decodeversion，然后再转换为 into吗？
		if err := c.convertor.Convert(obj, into, c.decodeVersion); err != nil {
			return nil, gvk, err
		}

		return into, gvk, nil
	}

	// perform defaulting if requested
	if c.defaulter != nil {
		c.defaulter.Default(obj)
	}

	// 此时，into 值为 nil
	// ConvertToVersion：将传入的（in）资源对象转换成目标（target）资源版本，在版本转换之前，会将资源对象深复制一份后再执行转换操作，相当于安全的内存对象转换操作
	out, err := c.convertor.ConvertToVersion(obj, c.decodeVersion)
	if err != nil {
		return nil, gvk, err
	}
	return out, gvk, nil
}

// Encode ensures the provided object is output in the appropriate group and version, invoking
// conversion if necessary. Unversioned objects (according to the ObjectTyper) are output as is.
func (c *codec) Encode(obj runtime.Object, w io.Writer) error {
	if co, ok := obj.(runtime.CacheableObject); ok {
		return co.CacheEncode(c.Identifier(), c.doEncode, w)
	}
	return c.doEncode(obj, w)
}

func (c *codec) doEncode(obj runtime.Object, w io.Writer) error {
	switch obj := obj.(type) {
	// TODO：unknown 表示什么？
	case *runtime.Unknown:
		return c.encoder.Encode(obj, w)
	//	TODO：unstructured 表示的是 cr 资源么？
	case runtime.Unstructured:
		// An unstructured list can contain objects of multiple group version kinds. don't short-circuit just
		// because the top-level type matches our desired destination type. actually send the object to the converter
		// to give it a chance to convert the list items if needed.
		if _, ok := obj.(*unstructured.UnstructuredList); !ok {
			// avoid conversion roundtrip if GVK is the right one already or is empty (yes, this is a hack, but the old behaviour we rely on in kubectl)
			objGVK := obj.GetObjectKind().GroupVersionKind()
			if len(objGVK.Version) == 0 {
				return c.encoder.Encode(obj, w)
			}
			// 判断此类 gvk 资源，是否能够用本 codec 进行编解码
			// TODO：codec 如果没有字段encodeVersion，那它应该可以解码所有的 gvk 资源，这里为什么要做限制呢？
			targetGVK, ok := c.encodeVersion.KindForGroupVersionKinds([]schema.GroupVersionKind{objGVK})
			if !ok {
				return runtime.NewNotRegisteredGVKErrForTarget(c.originalSchemeName, objGVK, c.encodeVersion)
			}
			if targetGVK == objGVK {
				return c.encoder.Encode(obj, w)
			}
		}
	}

	// 从 obj 中获取 gvk 信息
	gvks, isUnversioned, err := c.typer.ObjectKinds(obj)
	if err != nil {
		return err
	}

	objectKind := obj.GetObjectKind()
	old := objectKind.GroupVersionKind()
	// restore the old GVK after encoding
	defer objectKind.SetGroupVersionKind(old)

	if c.encodeVersion == nil || isUnversioned {
		if e, ok := obj.(runtime.NestedObjectEncoder); ok {
			if err := e.EncodeNestedObjects(runtime.WithVersionEncoder{Encoder: c.encoder, ObjectTyper: c.typer}); err != nil {
				return err
			}
		}
		objectKind.SetGroupVersionKind(gvks[0])
		return c.encoder.Encode(obj, w)
	}

	// Perform a conversion if necessary
	out, err := c.convertor.ConvertToVersion(obj, c.encodeVersion)
	if err != nil {
		return err
	}

	if e, ok := out.(runtime.NestedObjectEncoder); ok {
		if err := e.EncodeNestedObjects(runtime.WithVersionEncoder{Version: c.encodeVersion, Encoder: c.encoder, ObjectTyper: c.typer}); err != nil {
			return err
		}
	}

	// Conversion is responsible for setting the proper group, version, and kind onto the outgoing object
	return c.encoder.Encode(out, w)
}

// Identifier implements runtime.Encoder interface.
func (c *codec) Identifier() runtime.Identifier {
	return c.identifier
}
