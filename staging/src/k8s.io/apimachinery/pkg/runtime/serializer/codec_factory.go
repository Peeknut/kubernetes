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

package serializer

import (
	"mime"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/runtime/serializer/protobuf"
	"k8s.io/apimachinery/pkg/runtime/serializer/recognizer"
	"k8s.io/apimachinery/pkg/runtime/serializer/versioning"
)

// serializerExtensions are for serializers that are conditionally compiled in
var serializerExtensions = []func(*runtime.Scheme) (serializerType, bool){}

type serializerType struct {
	AcceptContentTypes []string
	ContentType        string
	FileExtensions     []string
	// EncodesAsText should be true if this content type can be represented safely in UTF-8
	EncodesAsText bool

	Serializer       runtime.Serializer
	// 根据 serializer option 中的字段 pretty 来配置（是否可读性友好）
	PrettySerializer runtime.Serializer

	AcceptStreamContentTypes []string
	StreamContentType        string

	// 接口：用于获取序列化格式（json、protobuf、yaml）的 reader & writers
	Framer           runtime.Framer
	// 根据 serializer option 来配置
	StreamSerializer runtime.Serializer
}

// 创建3种格式的 serializer（json、protobuf、yaml）
func newSerializersForScheme(scheme *runtime.Scheme, mf json.MetaFactory, options CodecFactoryOptions) []serializerType {
	// step1：创建 json serializer
	jsonSerializer := json.NewSerializerWithOptions(
		mf, scheme, scheme,
		json.SerializerOptions{Yaml: false, Pretty: false, Strict: options.Strict},
	)
	jsonSerializerType := serializerType{
		AcceptContentTypes: []string{runtime.ContentTypeJSON},
		ContentType:        runtime.ContentTypeJSON,
		FileExtensions:     []string{"json"},
		EncodesAsText:      true,
		// TODO：Serializer 和 StreamSerializer 字段的差别是什么？
		Serializer:         jsonSerializer,

		Framer:           json.Framer,
		StreamSerializer: jsonSerializer,
	}
	// TODO：为什么这里还需要创建一个 pretty 的？因为 serializer 创建之后 options 就不能变了么？
	if options.Pretty {
		jsonSerializerType.PrettySerializer = json.NewSerializerWithOptions(
			mf, scheme, scheme,
			json.SerializerOptions{Yaml: false, Pretty: true, Strict: options.Strict},
		)
	}

	// step2：创建 yaml serializer
	yamlSerializer := json.NewSerializerWithOptions(
		mf, scheme, scheme,
		json.SerializerOptions{Yaml: true, Pretty: false, Strict: options.Strict},
	)
	// step3：创建 protobuf serializer
	protoSerializer := protobuf.NewSerializer(scheme, scheme)
	protoRawSerializer := protobuf.NewRawSerializer(scheme, scheme)

	serializers := []serializerType{
		jsonSerializerType,
		{
			AcceptContentTypes: []string{runtime.ContentTypeYAML},
			ContentType:        runtime.ContentTypeYAML,
			FileExtensions:     []string{"yaml"},
			EncodesAsText:      true,
			Serializer:         yamlSerializer,
			// TODO：这里 yaml serializer 不设置 framer，是不是对应 recognizingDecoder 中，yaml 格式的返回的是 unknown？
		},
		{
			AcceptContentTypes: []string{runtime.ContentTypeProtobuf},
			ContentType:        runtime.ContentTypeProtobuf,
			FileExtensions:     []string{"pb"},
			Serializer:         protoSerializer,

			Framer:           protobuf.LengthDelimitedFramer,
			StreamSerializer: protoRawSerializer,
		},
	}

	// TODO：serializerExtensions 中是什么，这里好像是空的？或者是用户自己扩展的？
	for _, fn := range serializerExtensions {
		if serializer, ok := fn(scheme); ok {
			serializers = append(serializers, serializer)
		}
	}
	return serializers
}

// 提供了 获取某个版本以及 content types（json 等）的 codecs（编解码器）以及 serializer（序列化器）的方法
// CodecFactory provides methods for retrieving codecs and serializers for specific
// versions and content types.
type CodecFactory struct {
	// 资源注册表
	scheme    *runtime.Scheme
	// TOD：为什么只有 decoder 方法，没有 encoder 方法？——有的，在 accepts 字段中
	// 万能解码器，不用根据序列化格式类型初始化具体的类型，用它就可以自动识别序列化格式类型并进行解码
	universal runtime.Decoder
	// 各个类型（json、pb、yaml）的 serializer（包含 decoder/encoder） 也在这里
	accepts   []runtime.SerializerInfo

	// TODO：这个字段的作用是什么？（如果有 json serializer 会使用，没有的话，用第一个 serializer 作为他的值）
	legacySerializer runtime.Serializer
}

// 含义参考：staging/src/k8s.io/apimachinery/pkg/runtime/serializer/json/json.go：SerializerOptions
// CodecFactoryOptions holds the options for configuring CodecFactory behavior
type CodecFactoryOptions struct {
	// Strict configures all serializers in strict mode
	Strict bool
	// Pretty includes a pretty serializer along with the non-pretty one
	Pretty bool
}

// 实现了这个函数的传入 NewCodecFactory() 构造函数
// CodecFactoryOptionsMutator takes a pointer to an options struct and then modifies it.
// Functions implementing this type can be passed to the NewCodecFactory() constructor.
type CodecFactoryOptionsMutator func(*CodecFactoryOptions)

// 以下4个函数用于打开/关闭 codecFactory 的配置
// EnablePretty enables including a pretty serializer along with the non-pretty one
func EnablePretty(options *CodecFactoryOptions) {
	options.Pretty = true
}

// DisablePretty disables including a pretty serializer along with the non-pretty one
func DisablePretty(options *CodecFactoryOptions) {
	options.Pretty = false
}

// EnableStrict enables configuring all serializers in strict mode
func EnableStrict(options *CodecFactoryOptions) {
	options.Strict = true
}

// DisableStrict disables configuring all serializers in strict mode
func DisableStrict(options *CodecFactoryOptions) {
	options.Strict = false
}

// NewCodecFactory provides methods for retrieving serializers for the supported wire formats
// and conversion wrappers to define preferred internal and external versions. In the future,
// as the internal version is used less, callers may instead use a defaulting serializer and
// only convert objects which are shared internally (Status, common API machinery).
//
// Mutators can be passed to change the CodecFactoryOptions before construction of the factory.
// It is recommended to explicitly pass mutators instead of relying on defaults.
// By default, Pretty is enabled -- this is conformant with previously supported behavior.
//
// TODO: allow other codecs to be compiled in?
// TODO: accept a scheme interface
func NewCodecFactory(scheme *runtime.Scheme, mutators ...CodecFactoryOptionsMutator) CodecFactory {
	options := CodecFactoryOptions{Pretty: true}
	for _, fn := range mutators {
		fn(&options)
	}

	serializers := newSerializersForScheme(scheme, json.DefaultMetaFactory, options)
	return newCodecFactory(scheme, serializers)
}

// newCodecFactory is a helper for testing that allows a different metafactory to be specified.
func newCodecFactory(scheme *runtime.Scheme, serializers []serializerType) CodecFactory {
	decoders := make([]runtime.Decoder, 0, len(serializers))
	var accepts []runtime.SerializerInfo
	alreadyAccepted := make(map[string]struct{})

	var legacySerializer runtime.Serializer
	for _, d := range serializers {
		decoders = append(decoders, d.Serializer)
		for _, mediaType := range d.AcceptContentTypes {
			// 多个acceptContentTypes 相同的 serializer，只选择一个
			if _, ok := alreadyAccepted[mediaType]; ok {
				continue
			}
			alreadyAccepted[mediaType] = struct{}{}
			info := runtime.SerializerInfo{
				MediaType:        d.ContentType,
				EncodesAsText:    d.EncodesAsText,
				Serializer:       d.Serializer,
				PrettySerializer: d.PrettySerializer,
			}

			mediaType, _, err := mime.ParseMediaType(info.MediaType)
			if err != nil {
				panic(err)
			}
			parts := strings.SplitN(mediaType, "/", 2)
			// 一般为 application
			info.MediaTypeType = parts[0]
			// 一般为 json/protobuf/yaml
			info.MediaTypeSubType = parts[1]

			if d.StreamSerializer != nil {
				info.StreamSerializer = &runtime.StreamSerializerInfo{
					Serializer:    d.StreamSerializer,
					EncodesAsText: d.EncodesAsText,
					Framer:        d.Framer,
				}
			}
			accepts = append(accepts, info)
			// 默认使用 json serializer 作为 legacySerializer
			if mediaType == runtime.ContentTypeJSON {
				legacySerializer = d.Serializer
			}
		}
	}
	// 如果没有 json serializer，那么使用第一个 serializer 作为 legacySerializer
	if legacySerializer == nil {
		legacySerializer = serializers[0].Serializer
	}

	return CodecFactory{
		scheme:    scheme,

		universal: recognizer.NewDecoder(decoders...),

		accepts: accepts,

		legacySerializer: legacySerializer,
	}
}

// WithoutConversion returns a NegotiatedSerializer that performs no conversion, even if the
// caller requests it.
func (f CodecFactory) WithoutConversion() runtime.NegotiatedSerializer {
	return WithoutConversionCodecFactory{f}
}

// SupportedMediaTypes returns the RFC2046 media types that this factory has serializers for.
func (f CodecFactory) SupportedMediaTypes() []runtime.SerializerInfo {
	return f.accepts
}

// 序列化（encode）为一个给定的 API 版本，反序列化为 internal 版本。
// 这里返回的 编解码器（codec）总是序列化为 json 。
// LegacyCodec encodes output to a given API versions, and decodes output into the internal form from
// any recognized source. The returned codec will always encode output to JSON. If a type is not
// found in the list of versions an error will be returned.
//
// This method is deprecated - clients and servers should negotiate a serializer by mime-type and
// invoke CodecForVersions. Callers that need only to read data should use UniversalDecoder().
//
// TODO: make this call exist only in pkg/api, and initialize it with the set of default versions.
//   All other callers will be forced to request a Codec directly.
func (f CodecFactory) LegacyCodec(version ...schema.GroupVersion) runtime.Codec {
	return versioning.NewDefaultingCodecForScheme(f.scheme, f.legacySerializer, f.universal, schema.GroupVersions(version), runtime.InternalGroupVersioner)
}

// 转换存储的对象为 go object
// UniversalDeserializer can convert any stored data recognized by this factory into a Go object that satisfies
// runtime.Object. It does not perform conversion. It does not perform defaulting.
func (f CodecFactory) UniversalDeserializer() runtime.Decoder {
	return f.universal
}

// 仅解码在方案中注册的组中的对象。 传递的 GroupVersions 可用于选择要返回的对象的替代版本
//- 默认情况下，使用 runtime.APIVersionInternal。 如果指定了任何版本，未识别的组将在它们被编码为的版本中返回（无转换）。 此解码器执行默认设置。
// UniversalDecoder returns a runtime.Decoder capable of decoding all known API objects in all known formats. Used
// by clients that do not need to encode objects but want to deserialize API objects stored on disk. Only decodes
// objects in groups registered with the scheme. The GroupVersions passed may be used to select alternate
// versions of objects to return - by default, runtime.APIVersionInternal is used. If any versions are specified,
// unrecognized groups will be returned in the version they are encoded as (no conversion). This decoder performs
// defaulting.
//
// TODO: the decoder will eventually be removed in favor of dealing with objects in their versioned form
// TODO: only accept a group versioner
func (f CodecFactory) UniversalDecoder(versions ...schema.GroupVersion) runtime.Decoder {
	var versioner runtime.GroupVersioner
	if len(versions) == 0 {
		versioner = runtime.InternalGroupVersioner
	} else {
		versioner = schema.GroupVersions(versions)
	}
	return f.CodecForVersions(nil, f.universal, nil, versioner)
}

// 根据传入的序列化器创建编解码器。如果反序列化的group 信息不在list中，则默认使用 internal 版本。如果序列化没有指定group，那么对象不会被转换
// CodecForVersions creates a codec with the provided serializer. If an object is decoded and its group is not in the list,
// it will default to runtime.APIVersionInternal. If encode is not specified for an object's group, the object is not
// converted. If encode or decode are nil, no conversion is performed.
func (f CodecFactory) CodecForVersions(encoder runtime.Encoder, decoder runtime.Decoder, encode runtime.GroupVersioner, decode runtime.GroupVersioner) runtime.Codec {
	// TODO: these are for backcompat, remove them in the future
	if encode == nil {
		encode = runtime.DisabledGroupVersioner
	}
	if decode == nil {
		decode = runtime.InternalGroupVersioner
	}
	return versioning.NewDefaultingCodecForScheme(f.scheme, encoder, decoder, encode, decode)
}

// 反序列化为给定gv的 decoder，如果gv没有给定，默认为 internal 版本
// DecoderToVersion returns a decoder that targets the provided group version.
func (f CodecFactory) DecoderToVersion(decoder runtime.Decoder, gv runtime.GroupVersioner) runtime.Decoder {
	return f.CodecForVersions(nil, decoder, nil, gv)
}

// 序列化给定 gv 的 encoder，如果 gv 没有给定，默认为 disable
// TODO：runtime.DisabledGroupVersioner 是什么类型？
// 		输入的 encoder 和输出的 ecoder 的差别是什么？为什么需要输入参数 encoder？
// EncoderForVersion returns an encoder that targets the provided group version.
func (f CodecFactory) EncoderForVersion(encoder runtime.Encoder, gv runtime.GroupVersioner) runtime.Encoder {
	return f.CodecForVersions(encoder, nil, gv, nil)
}

//WithConversionCodecFactory 是一个 CodecFactory（继承了 CodecFactory，而不是将 CodecFactory 作为其成员），它将显式忽略执行转换的请求。
// WithoutConversionCodecFactory is a CodecFactory that will explicitly ignore requests to perform conversion.
// This wrapper is used while code migrates away from using conversion (such as external clients) and in the future
// will be unnecessary when we change the signature of NegotiatedSerializer.
type WithoutConversionCodecFactory struct {
	CodecFactory
}

// EncoderForVersion returns an encoder that does not do conversion, but does set the group version kind of the object
// when serialized.
func (f WithoutConversionCodecFactory) EncoderForVersion(serializer runtime.Encoder, version runtime.GroupVersioner) runtime.Encoder {
	return runtime.WithVersionEncoder{
		Version:     version,
		Encoder:     serializer,
		ObjectTyper: f.CodecFactory.scheme,
	}
}

// DecoderToVersion returns an decoder that does not do conversion.
func (f WithoutConversionCodecFactory) DecoderToVersion(serializer runtime.Decoder, _ runtime.GroupVersioner) runtime.Decoder {
	return runtime.WithoutVersionDecoder{
		Decoder: serializer,
	}
}
