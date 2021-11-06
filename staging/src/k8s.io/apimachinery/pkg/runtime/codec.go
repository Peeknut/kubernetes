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

package runtime

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/conversion/queryparams"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

// TODO：和 serializer interface 的差别在哪里
// A：编解码器，serializer 是 codec 的一种（serializer 将对象转换为字符串）
// 我们把interface作为struct的一个匿名成员，就可以假设struct就是此成员interface的一个实现，而不管struct是否已经实现interface所定义的函数。
//嵌入interface可以使得一个struct具有interface的接口，而不需要实现interface中的有声明的函数
// codec binds an encoder and decoder.
type codec struct {
	Encoder
	Decoder
}

// NewCodec creates a Codec from an Encoder and Decoder.
func NewCodec(e Encoder, d Decoder) Codec {
	return codec{e, d}
}

// Encode is a convenience wrapper for encoding to a []byte from an Encoder
func Encode(e Encoder, obj Object) ([]byte, error) {
	// TODO: reuse buffer
	buf := &bytes.Buffer{}
	if err := e.Encode(obj, buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decode is a convenience wrapper for decoding data into an Object.
func Decode(d Decoder, data []byte) (Object, error) {
	// Decode 如果没有传入后两个参数，则要转换的目的 obj 的 gvk 信息会从 data 中获取
	obj, _, err := d.Decode(data, nil, nil)
	return obj, err
}

// DecodeInto performs a Decode into the provided object.
func DecodeInto(d Decoder, data []byte, into Object) error {
	out, gvk, err := d.Decode(data, nil, into)
	if err != nil {
		return err
	}
	// 如果 out ！= into 表示传入的 into 类型与 data 本身的 gvk 类型不匹配
	if out != into {
		return fmt.Errorf("unable to decode %s into %v", gvk, reflect.TypeOf(into))
	}
	return nil
}

// EncodeOrDie is a version of Encode which will panic instead of returning an error. For tests.
func EncodeOrDie(e Encoder, obj Object) string {
	bytes, err := Encode(e, obj)
	if err != nil {
		panic(err)
	}
	return string(bytes)
}

// UseOrCreateObject returns obj if the canonical ObjectKind returned by the provided typer matches gvk, or
// invokes the ObjectCreator to instantiate a new gvk. Returns an error if the typer cannot find the object.
func UseOrCreateObject(t ObjectTyper, c ObjectCreater, gvk schema.GroupVersionKind, obj Object) (Object, error) {
	if obj != nil {
		kinds, _, err := t.ObjectKinds(obj)
		if err != nil {
			return nil, err
		}
		for _, kind := range kinds {
			if gvk == kind {
				return obj, nil
			}
		}
	}
	return c.New(gvk)
}

// 将 Decoder 转换为 Serializer/Codec，但是转合后的 serializer/codec 只能使用 decoding
// NoopEncoder converts an Decoder to a Serializer or Codec for code that expects them but only uses decoding.
type NoopEncoder struct {
	Decoder
}

// TODO：为什么这里要有一个匿名变量——断言 NoopEncoder 是否实现了接口 Serializer
var _ Serializer = NoopEncoder{}

const noopEncoderIdentifier Identifier = "noop"

func (n NoopEncoder) Encode(obj Object, w io.Writer) error {
	// There is no need to handle runtime.CacheableObject, as we don't
	// process the obj at all.
	return fmt.Errorf("encoding is not allowed for this codec: %v", reflect.TypeOf(n.Decoder))
}

// Identifier implements runtime.Encoder interface.
func (n NoopEncoder) Identifier() Identifier {
	return noopEncoderIdentifier
}

// NoopDecoder converts an Encoder to a Serializer or Codec for code that expects them but only uses encoding.
type NoopDecoder struct {
	Encoder
}

var _ Serializer = NoopDecoder{}

func (n NoopDecoder) Decode(data []byte, gvk *schema.GroupVersionKind, into Object) (Object, *schema.GroupVersionKind, error) {
	return nil, nil, fmt.Errorf("decoding is not allowed for this codec: %v", reflect.TypeOf(n.Encoder))
}

// NewParameterCodec creates a ParameterCodec capable of transforming url values into versioned objects and back.
func NewParameterCodec(scheme *Scheme) ParameterCodec {
	return &parameterCodec{
		typer:     scheme,
		convertor: scheme,
		creator:   scheme,
		defaulter: scheme,
	}
}

// parameterCodec 实现与查询参数和对象的转换
// parameterCodec implements conversion to and from query parameters and objects.
type parameterCodec struct {
	// 用来根据 obj 确定资源的类型 gvk
	typer     ObjectTyper
	// 用于资源的正常版本和内部版本之间的相互转换
	convertor ObjectConvertor
	// 用于资源在 decode 操作中正常版本的创建
	creator   ObjectCreater
	// 用于资源在 decode 操作中正常版本的默认值创建
	defaulter ObjectDefaulter
}

var _ ParameterCodec = &parameterCodec{}

// TODO：原始版本和目的版本是什么？——目的gvk信息：gv 信息：参数 from；kind 信息：参数into中获得
// 将 url.Values 转换为 type 为 from，kind 为 into 的 obj。然后将 obj 转换为 into
// DecodeParameters converts the provided url.Values into an object of type From with the kind of into, and then
// converts that object to into (if necessary). Returns an error if the operation cannot be completed.
func (c *parameterCodec) DecodeParameters(parameters url.Values, from schema.GroupVersion, into Object) error {
	if len(parameters) == 0 {
		return nil
	}
	targetGVKs, _, err := c.typer.ObjectKinds(into)
	if err != nil {
		return err
	}
	// 如果存在 gvk 信息匹配的，转换为 into
	for i := range targetGVKs {
		if targetGVKs[i].GroupVersion() == from {
			// 将 parameters 转换为 into
			if err := c.convertor.Convert(&parameters, into, nil); err != nil {
				return err
			}
			// 对于没有设置的字段设置默认值
			// in the case where we going into the same object we're receiving, default on the outbound object
			if c.defaulter != nil {
				c.defaulter.Default(into)
			}
			return nil
		}
	}

	// 感觉可能认为用户输入参数 from 有时候会错误，所以使用 kind 得到的第一个 gv 信息，尝试进行转换
	input, err := c.creator.New(from.WithKind(targetGVKs[0].Kind))
	if err != nil {
		return err
	}
	if err := c.convertor.Convert(&parameters, input, nil); err != nil {
		return err
	}
	// if we have defaulter, default the input before converting to output
	if c.defaulter != nil {
		c.defaulter.Default(input)
	}
	// 转换 input 为 into
	// TODO：为什么需要进行这一步的转换
	return c.convertor.Convert(input, into, nil)
}

// 将 obj 转换为 to 版本的 obj，然后将 to 版本的 obj 转换为 url.Values
// 不同版本之间的转换 kind 是相同的，gv 信息不同
// EncodeParameters converts the provided object into the to version, then converts that object to url.Values.
// Returns an error if conversion is not possible.
func (c *parameterCodec) EncodeParameters(obj Object, to schema.GroupVersion) (url.Values, error) {
	gvks, _, err := c.typer.ObjectKinds(obj)
	if err != nil {
		return nil, err
	}
	gvk := gvks[0]
	// 如果目的版本 to 与 obj 现在的版本不同，则进行转换
	if to != gvk.GroupVersion() {
		out, err := c.convertor.ConvertToVersion(obj, to)
		if err != nil {
			return nil, err
		}
		// obj 赋值为最终的目的版本
		obj = out
	}
	// obj 转换为 map[string][]string 类型
	return queryparams.Convert(obj)
}

type base64Serializer struct {
	Encoder
	Decoder

	identifier Identifier
}

func NewBase64Serializer(e Encoder, d Decoder) Serializer {
	return &base64Serializer{
		Encoder:    e,
		Decoder:    d,
		identifier: identifier(e),
	}
}

func identifier(e Encoder) Identifier {
	result := map[string]string{
		"name": "base64",
	}
	if e != nil {
		result["encoder"] = string(e.Identifier())
	}
	identifier, err := json.Marshal(result)
	if err != nil {
		klog.Fatalf("Failed marshaling identifier for base64Serializer: %v", err)
	}
	return Identifier(identifier)
}

func (s base64Serializer) Encode(obj Object, stream io.Writer) error {
	if co, ok := obj.(CacheableObject); ok {
		return co.CacheEncode(s.Identifier(), s.doEncode, stream)
	}
	return s.doEncode(obj, stream)
}

func (s base64Serializer) doEncode(obj Object, stream io.Writer) error {
	e := base64.NewEncoder(base64.StdEncoding, stream)
	err := s.Encoder.Encode(obj, e)
	e.Close()
	return err
}

// Identifier implements runtime.Encoder interface.
func (s base64Serializer) Identifier() Identifier {
	return s.identifier
}

func (s base64Serializer) Decode(data []byte, defaults *schema.GroupVersionKind, into Object) (Object, *schema.GroupVersionKind, error) {
	out := make([]byte, base64.StdEncoding.DecodedLen(len(data)))
	n, err := base64.StdEncoding.Decode(out, data)
	if err != nil {
		return nil, nil, err
	}
	return s.Decoder.Decode(out[:n], defaults, into)
}

// 判断 types 中是否与 mediaType 匹配
// SerializerInfoForMediaType returns the first info in types that has a matching media type (which cannot
// include media-type parameters), or the first info with an empty media type, or false if no type matches.
func SerializerInfoForMediaType(types []SerializerInfo, mediaType string) (SerializerInfo, bool) {
	for _, info := range types {
		if info.MediaType == mediaType {
			return info, true
		}
	}
	for _, info := range types {
		if len(info.MediaType) == 0 {
			return info, true
		}
	}
	return SerializerInfo{}, false
}

var (
	// InternalGroupVersioner will always prefer the internal version for a given group version kind.
	InternalGroupVersioner GroupVersioner = internalGroupVersioner{}
	// DisabledGroupVersioner will reject all kinds passed to it.
	DisabledGroupVersioner GroupVersioner = disabledGroupVersioner{}
)

const (
	internalGroupVersionerIdentifier = "internal"
	disabledGroupVersionerIdentifier = "disabled"
)

type internalGroupVersioner struct{}

// 将 kinds 中的 version 修改为 internal
// KindForGroupVersionKinds returns an internal Kind if one is found, or converts the first provided kind to the internal version.
func (internalGroupVersioner) KindForGroupVersionKinds(kinds []schema.GroupVersionKind) (schema.GroupVersionKind, bool) {
	for _, kind := range kinds {
		if kind.Version == APIVersionInternal {
			return kind, true
		}
	}
	for _, kind := range kinds {
		return schema.GroupVersionKind{Group: kind.Group, Version: APIVersionInternal, Kind: kind.Kind}, true
	}
	return schema.GroupVersionKind{}, false
}

// Identifier implements GroupVersioner interface.
func (internalGroupVersioner) Identifier() string {
	return internalGroupVersionerIdentifier
}

type disabledGroupVersioner struct{}

// KindForGroupVersionKinds returns false for any input.
func (disabledGroupVersioner) KindForGroupVersionKinds(kinds []schema.GroupVersionKind) (schema.GroupVersionKind, bool) {
	return schema.GroupVersionKind{}, false
}

// Identifier implements GroupVersioner interface.
func (disabledGroupVersioner) Identifier() string {
	return disabledGroupVersionerIdentifier
}

// 断言 schema.GroupVersion、schema.GroupVersions 实现了接口 GroupVersioner
// Assert that schema.GroupVersion and GroupVersions implement GroupVersioner
var _ GroupVersioner = schema.GroupVersion{}
var _ GroupVersioner = schema.GroupVersions{}
var _ GroupVersioner = multiGroupVersioner{}

// 同一GV 信息，不同 kind（比如 pod、node等）
type multiGroupVersioner struct {
	target             schema.GroupVersion
	acceptedGroupKinds []schema.GroupKind
	coerce             bool
}

// NewMultiGroupVersioner returns the provided group version for any kind that matches one of the provided group kinds.
// Kind may be empty in the provided group kind, in which case any kind will match.
func NewMultiGroupVersioner(gv schema.GroupVersion, groupKinds ...schema.GroupKind) GroupVersioner {
	if len(groupKinds) == 0 || (len(groupKinds) == 1 && groupKinds[0].Group == gv.Group) {
		return gv
	}
	return multiGroupVersioner{target: gv, acceptedGroupKinds: groupKinds}
}

// NewCoercingMultiGroupVersioner returns the provided group version for any incoming kind.
// Incoming kinds that match the provided groupKinds are preferred.
// Kind may be empty in the provided group kind, in which case any kind will match.
// Examples:
//   gv=mygroup/__internal, groupKinds=mygroup/Foo, anothergroup/Bar
//   KindForGroupVersionKinds(yetanother/v1/Baz, anothergroup/v1/Bar) -> mygroup/__internal/Bar (matched preferred group/kind)
//
//   gv=mygroup/__internal, groupKinds=mygroup, anothergroup
//   KindForGroupVersionKinds(yetanother/v1/Baz, anothergroup/v1/Bar) -> mygroup/__internal/Bar (matched preferred group)
//
//   gv=mygroup/__internal, groupKinds=mygroup, anothergroup
//   KindForGroupVersionKinds(yetanother/v1/Baz, yetanother/v1/Bar) -> mygroup/__internal/Baz (no preferred group/kind match, uses first kind in list)
func NewCoercingMultiGroupVersioner(gv schema.GroupVersion, groupKinds ...schema.GroupKind) GroupVersioner {
	return multiGroupVersioner{target: gv, acceptedGroupKinds: groupKinds, coerce: true}
}

// 返回 gvk 信息，如果 kinds 中有匹配的 kind
// KindForGroupVersionKinds returns the target group version if any kind matches any of the original group kinds. It will
// use the originating kind where possible.
func (v multiGroupVersioner) KindForGroupVersionKinds(kinds []schema.GroupVersionKind) (schema.GroupVersionKind, bool) {
	// 比较输入的 gvk 信息与 multiGroupVersioner 中 gvk 信息
	for _, src := range kinds {
		for _, kind := range v.acceptedGroupKinds {
			if kind.Group != src.Group {
				continue
			}
			// 不是core组，并且不相等
			if len(kind.Kind) > 0 && kind.Kind != src.Kind {
				continue
			}
			// Group 相同，并且 kind 相同（kind 不为空）
			return v.target.WithKind(src.Kind), true
		}
	}
	if v.coerce && len(kinds) > 0 {
		return v.target.WithKind(kinds[0].Kind), true
	}
	return schema.GroupVersionKind{}, false
}

// Identifier implements GroupVersioner interface.
func (v multiGroupVersioner) Identifier() string {
	groupKinds := make([]string, 0, len(v.acceptedGroupKinds))
	for _, gk := range v.acceptedGroupKinds {
		groupKinds = append(groupKinds, gk.String())
	}
	result := map[string]string{
		"name":     "multi",
		"target":   v.target.String(),
		"accepted": strings.Join(groupKinds, ","),
		"coerce":   strconv.FormatBool(v.coerce),
	}
	identifier, err := json.Marshal(result)
	if err != nil {
		klog.Fatalf("Failed marshaling Identifier for %#v: %v", v, err)
	}
	return string(identifier)
}
