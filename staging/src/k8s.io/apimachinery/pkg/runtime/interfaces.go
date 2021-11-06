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
	"io"
	"net/url"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// APIVersionInternal may be used if you are registering a type that should not
	// be considered stable or serialized - it is a convention only and has no
	// special behavior in this package.
	APIVersionInternal = "__internal"
)

// TODO：这个接口的作用是什么？——将传入的 kinds 转换为某个gvk
// GroupVersioner refines a set of possible conversion targets into a single option.
type GroupVersioner interface {
	// KindForGroupVersionKinds returns a desired target group version kind for the given input, or returns ok false if no
	// target is known. In general, if the return target is not in the input list, the caller is expected to invoke
	// Scheme.New(target) and then perform a conversion between the current Go type and the destination Go type.
	// Sophisticated implementations may use additional information about the input kinds to pick a destination kind.
	KindForGroupVersionKinds(kinds []schema.GroupVersionKind) (target schema.GroupVersionKind, ok bool)
	// Identifier returns string representation of the object.
	// Identifiers of two different encoders should be equal only if for every input
	// kinds they return the same result.
	Identifier() string
}

// 标识符就是字符串，可以简单的理解为标签的字符串形式，后面会看到如何生成标识符。
// Identifier represents an identifier.
// Identitier of two different objects should be equal if and only if for every
// input the output they produce is exactly the same.
type Identifier string

// 序列化的过程称之为编码，实现编码的对象称之为编码器(Encoder)
// Encoder writes objects to a serialized form
type Encoder interface {
	// Encode writes an object to a stream. Implementations may return errors if the versions are
	// incompatible, or if no conversion is defined.
	Encode(obj Object, w io.Writer) error
	// Identifier returns an identifier of the encoder.
	// Identifiers of two different encoders should be equal if and only if for every input
	// object it will be encoded to the same representation by both of them.
	//
	// Identifier is intended for use with CacheableObject#CacheEncode method. In order to
	// correctly handle CacheableObject, Encode() method should look similar to below, where
	// doEncode() is the encoding logic of implemented encoder:
	//   func (e *MyEncoder) Encode(obj Object, w io.Writer) error {
	//     if co, ok := obj.(CacheableObject); ok {
	//       return co.CacheEncode(e.Identifier(), e.doEncode, w)
	//     }
	//     return e.doEncode(obj, w)
	//   }
	// Identifier()返回编码器的标识符，当且仅当两个不同的编码器编码同一个对象的输出是相同的，那么这两个编码器的标识符也应该是相同的。
	// 也就是说，编码器都有一个标识符，两个编码器的标识符可能是相同的，判断标准是编码任意API对象时输出都是相同的。
	// 标识符有什么用？标识符目标是与CacheableObject.CacheEncode()方法一起使用，CacheableObject又是什么东东？后面有介绍。
	Identifier() Identifier
}


// 反序列化的过程称之为解码，实现解码的对象称之为解码器(Decoder)
// Decoder attempts to load an object from data.
type Decoder interface {
	// Decode attempts to deserialize the provided data using either the innate typing of the scheme or the
	// default kind, group, and version provided. It returns a decoded object as well as the kind, group, and
	// version from the serialized data, or an error. If into is non-nil, it will be used as the target type
	// and implementations may choose to use it rather than reallocating an object. However, the object is not
	// guaranteed to be populated. The returned object is not guaranteed to match into. If defaults are
	// provided, they are applied to the data by default. If no defaults or partial defaults are provided, the
	// type of the into may be used to guide conversion decisions.
	// Decode()尝试使用Schema中注册的类型或者提供的默认的GVK反序列化API对象。
	// 如果'into'非空将被用作目标类型，接口实现可能会选择使用它而不是重新构造一个对象。
	// 但是不能保证输出到'into'指向的对象，因为返回的对象不保证匹配'into'。
	// 如果提供了默认GVK，将应用默认GVK反序列化，如果未提供默认GVK或仅提供部分，则使用'into'的类型补全。
	Decode(data []byte, defaults *schema.GroupVersionKind, into Object) (Object, *schema.GroupVersionKind, error)
}

// Serializer is the core interface for transforming objects into a serialized format and back.
// Implementations may choose to perform conversion of the object, but no assumptions should be made.
type Serializer interface {
	Encoder
	Decoder
}

// TODO：为什么要重新定义？
// 用于处理具体版本的对象的序列化/反序列化
// Codec is a Serializer that deals with the details of versioning objects. It offers the same
// interface as Serializer, so this is a marker to consumers that care about the version of the objects
// they receive.
type Codec Serializer

// TODO：使用的场合？为什么有了 Codec 之后还要有这个？
// 区别于 Codec，ParameterCodec 不会自己发现 gvk 信息，需要用户指定目标 gvk 信息
// ParameterCodec defines methods for serializing and deserializing API objects to url.Values and
// performing any necessary conversion. Unlike the normal Codec, query parameters are not self describing
// and the desired version must be specified.
type ParameterCodec interface {
	// DecodeParameters takes the given url.Values in the specified group version and decodes them
	// into the provided object, or returns an error.
	// 参数 from、into.Kind 组成目标 obj 的 gvk 信息
	DecodeParameters(parameters url.Values, from schema.GroupVersion, into Object) error
	// EncodeParameters encodes the provided object as query parameters or returns an error.
	// 参数 to 表示转换后的 gv。最终再将其转换为 url.Values
	EncodeParameters(obj Object, to schema.GroupVersion) (url.Values, error)
}
// Framer 是一个工厂，用于创建遵循特定框架模式的 reader & writer。
// Framer is a factory for creating readers and writers that obey a particular framing pattern.
type Framer interface {
	NewFrameReader(r io.ReadCloser) io.ReadCloser
	NewFrameWriter(w io.Writer) io.Writer
}

// 特定序列化格式的序列化器的信息（一般有 json、protobuf、yaml 格式）
// SerializerInfo contains information about a specific serialization format
type SerializerInfo struct {
	// 比如：application/json
	// MediaType is the value that represents this serializer over the wire.
	MediaType string
	// 比如：application
	// MediaTypeType is the first part of the MediaType ("application" in "application/json").
	MediaTypeType string
	// 比如  json
	// MediaTypeSubType is the second part of the MediaType ("json" in "application/json").
	MediaTypeSubType string
	// 是否可以安全的编码为 utf-8 格式
	// EncodesAsText indicates this serializer can be encoded to UTF-8 safely.
	EncodesAsText bool
	// 对应格式（json、protobuf、yaml 等与 mediatype 对应的格式）的序列化器
	// Serializer is the individual object serializer for this media type.
	Serializer Serializer
	// 对应格式（json、protobuf、yaml 等与 mediatype 对应的格式）的序列化器——与 Serializer 的差别应该就是 pretty 字段设置不同
	// 只对 encode 起作用，encode之后的结果可读性更强，对 yaml 序列化器没有作用，因为yaml本身可读性就很强
	// PrettySerializer, if set, can serialize this object in a form biased towards
	// readability.
	PrettySerializer Serializer
	// 对应格式（json、protobuf、yaml 等与 mediatype 对应的格式）的序列化器——也是从 Serializer 生成的
	// StreamSerializer, if set, describes the streaming serialization format
	// for this media type.
	StreamSerializer *StreamSerializerInfo
}

// StreamSerializerInfo contains information about a specific stream serialization format
type StreamSerializerInfo struct {
	// EncodesAsText indicates this serializer can be encoded to UTF-8 safely.
	EncodesAsText bool
	// Serializer is the top level object serializer for this type when streaming
	Serializer
	// Framer is the factory for retrieving streams that separate objects on the wire
	Framer
}

// 这是所有 codec-factory 的接口。不同 codec-factory 用于生成不同类型的 codec
// 根据资源类型，获取对应的 encoder/decoder/serializer。
// 一般用于 server 解析 http 请求
// NegotiatedSerializer is an interface used for obtaining encoders, decoders, and serializers
// for multiple supported media types. This would commonly be accepted by a server component
// that performs HTTP content negotiation to accept multiple formats.
type NegotiatedSerializer interface {
	// 支持的序列化器
	// SupportedMediaTypes is the media types supported for reading and writing single objects.
	SupportedMediaTypes() []SerializerInfo

	// 返回可以解析某个 gv 的 encoder
	// EncoderForVersion returns an encoder that ensures objects being written to the provided
	// serializer are in the provided group version.
	EncoderForVersion(serializer Encoder, gv GroupVersioner) Encoder
	// 返回可以解析某个 gv 的 decoder
	// DecoderForVersion returns a decoder that ensures objects being read by the provided
	// serializer are in the provided group version by default.
	DecoderToVersion(serializer Decoder, gv GroupVersioner) Decoder
}

// TODO：这个是不是给 client-go 使用的？
// ClientNegotiator 处理将 HTTP 内容类型转换为适当的编码器。
// ClientNegotiator handles turning an HTTP content type into the appropriate encoder.
// Use NewClientNegotiator or NewVersionedClientNegotiator to create this interface from
// a NegotiatedSerializer.
type ClientNegotiator interface {
	// Encoder 根据 http 内容类型（即 contenttype 字段）以及 options（即类似 staging/src/k8s.io/apimachinery/pkg/runtime/serializer/json/json.go：SerializerOptions）
	// 获取合适的 encoder。
	// 当前 client 的实现将 parames 视为 contenttype 的可选修饰符，并将忽视无法识别的参数
	// Encoder returns the appropriate encoder for the provided contentType (e.g. application/json)
	// and any optional mediaType parameters (e.g. pretty=1), or an error. If no serializer is found
	// a NegotiateError will be returned. The current client implementations consider params to be
	// optional modifiers to the contentType and will ignore unrecognized parameters.
	Encoder(contentType string, params map[string]string) (Encoder, error)
	// Decoder returns the appropriate decoder for the provided contentType (e.g. application/json)
	// and any optional mediaType parameters (e.g. pretty=1), or an error. If no serializer is found
	// a NegotiateError will be returned. The current client implementations consider params to be
	// optional modifiers to the contentType and will ignore unrecognized parameters.
	Decoder(contentType string, params map[string]string) (Decoder, error)
	// StreamDecoder returns the appropriate stream decoder for the provided contentType (e.g.
	// application/json) and any optional mediaType parameters (e.g. pretty=1), or an error. If no
	// serializer is found a NegotiateError will be returned. The Serializer and Framer will always
	// be returned if a Decoder is returned. The current client implementations consider params to be
	// optional modifiers to the contentType and will ignore unrecognized parameters.
	StreamDecoder(contentType string, params map[string]string) (Decoder, Serializer, Framer, error)
}

// 返回可以读写静态数据的 encoder/decoder/serializer。
// 这通常由必须读取文件的客户端工具或持久化静态对象的服务器端存储接口使用。
// StorageSerializer is an interface used for obtaining encoders, decoders, and serializers
// that can read and write data at rest. This would commonly be used by client tools that must
// read files, or server side storage interfaces that persist restful objects.
type StorageSerializer interface {
	// SupportedMediaTypes are the media types supported for reading and writing objects.
	SupportedMediaTypes() []SerializerInfo

	// UniversalDeserializer returns a Serializer that can read objects in multiple supported formats
	// by introspecting the data at rest.
	UniversalDeserializer() Decoder

	// EncoderForVersion returns an encoder that ensures objects being written to the provided
	// serializer are in the provided group version.
	EncoderForVersion(serializer Encoder, gv GroupVersioner) Encoder
	// DecoderForVersion returns a decoder that ensures objects being read by the provided
	// serializer are in the provided group version by default.
	DecoderToVersion(serializer Decoder, gv GroupVersioner) Decoder
}

// Nested 嵌套
// 编码obj中嵌套的obj或者RawExtensions。如果有这个编码需求的，obj 需要实现这个接口
// NestedObjectEncoder is an optional interface that objects may implement to be given
// an opportunity to encode any nested Objects / RawExtensions during serialization.
type NestedObjectEncoder interface {
	EncodeNestedObjects(e Encoder) error
}

// NestedObjectDecoder is an optional interface that objects may implement to be given
// an opportunity to decode any nested Objects / RawExtensions during serialization.
type NestedObjectDecoder interface {
	DecodeNestedObjects(d Decoder) error
}

///////////////////////////////////////////////////////////////////////////////
// Non-codec interfaces
// 非编解码接口

type ObjectDefaulter interface {
	// Default takes an object (must be a pointer) and applies any default values.
	// Defaulters may not error.
	Default(in Object)
}

type ObjectVersioner interface {
	ConvertToVersion(in Object, gv GroupVersioner) (out Object, err error)
}

// ObjectConvertor converts an object to a different version.
type ObjectConvertor interface {
	// Convert attempts to convert one object into another, or returns an error. This
	// method does not mutate the in object, but the in and out object might share data structures,
	// i.e. the out object cannot be mutated without mutating the in object as well.
	// The context argument will be passed to all nested conversions.
	Convert(in, out, context interface{}) error
	//// ConvertToVersion：将传入的（in）资源对象转换成目标（target）资源版本，在版本转换之前，会将资源对象深复制一份后再执行转换操作，相当于安全的内存对象转换操作
	// ConvertToVersion takes the provided object and converts it the provided version. This
	// method does not mutate the in object, but the in and out object might share data structures,
	// i.e. the out object cannot be mutated without mutating the in object as well.
	// This method is similar to Convert() but handles specific details of choosing the correct
	// output version.
	ConvertToVersion(in Object, gv GroupVersioner) (out Object, err error)
	ConvertFieldLabel(gvk schema.GroupVersionKind, label, value string) (string, string, error)
}

// ObjectTyper contains methods for extracting the APIVersion and Kind
// of objects.
type ObjectTyper interface {
	// ObjectKinds returns the all possible group,version,kind of the provided object, true if
	// the object is unversioned, or an error if the object is not recognized
	// (IsNotRegisteredError will return true).
	ObjectKinds(Object) ([]schema.GroupVersionKind, bool, error)
	// Recognizes returns true if the scheme is able to handle the provided version and kind,
	// or more precisely that the provided version is a possible conversion or decoding
	// target.
	Recognizes(gvk schema.GroupVersionKind) bool
}

// ObjectCreater contains methods for instantiating an object by kind and version.
type ObjectCreater interface {
	New(kind schema.GroupVersionKind) (out Object, err error)
}

// EquivalentResourceMapper provides information about resources that address the same underlying data as a specified resource
type EquivalentResourceMapper interface {
	// EquivalentResourcesFor returns a list of resources that address the same underlying data as resource.
	// If subresource is specified, only equivalent resources which also have the same subresource are included.
	// The specified resource can be included in the returned list.
	EquivalentResourcesFor(resource schema.GroupVersionResource, subresource string) []schema.GroupVersionResource
	// KindFor returns the kind expected by the specified resource[/subresource].
	// A zero value is returned if the kind is unknown.
	KindFor(resource schema.GroupVersionResource, subresource string) schema.GroupVersionKind
}

// EquivalentResourceRegistry provides an EquivalentResourceMapper interface,
// and allows registering known resource[/subresource] -> kind
type EquivalentResourceRegistry interface {
	EquivalentResourceMapper
	// RegisterKindFor registers the existence of the specified resource[/subresource] along with its expected kind.
	RegisterKindFor(resource schema.GroupVersionResource, subresource string, kind schema.GroupVersionKind)
}

// ResourceVersioner provides methods for setting and retrieving
// the resource version from an API object.
type ResourceVersioner interface {
	SetResourceVersion(obj Object, version string) error
	ResourceVersion(obj Object) (string, error)
}

// SelfLinker provides methods for setting and retrieving the SelfLink field of an API object.
type SelfLinker interface {
	SetSelfLink(obj Object, selfLink string) error
	SelfLink(obj Object) (string, error)

	// Knowing Name is sometimes necessary to use a SelfLinker.
	Name(obj Object) (string, error)
	// Knowing Namespace is sometimes necessary to use a SelfLinker
	Namespace(obj Object) (string, error)
}

// Object interface must be supported by all API types registered with Scheme. Since objects in a scheme are
// expected to be serialized to the wire, the interface an Object must provide to the Scheme allows
// serializers to set the kind, version, and group the object is represented as. An Object may choose
// to return a no-op ObjectKindAccessor in cases where it is not expected to be serialized.
type Object interface {
	// 有了这个函数，就可以访问对象的类型域
	GetObjectKind() schema.ObjectKind
	// deepcopy是golang深度复制对象的方法，至于什么是深度复制本文就不解释了。这是个不错的函数，
	// 可以通过这个接口复制任何API对象而无需类型依赖
	//为什么没有看到API对象实现runtime.Object.DeepCopyObject()？那是因为deep copy是具体API对象类型需要实现的，存在类型依赖，作为API对象类型的父类不能实现。此处还是以Pod为例，看看Pod是如何实现DeepCopyObject()的。
	DeepCopyObject() Object
	// 就这么两个函数了么？那如果需要访问对象的公共属性域怎么办？不应该有一个类似GetObjectMeta()
	// 的接口么？这一点，kubernetes是通过另一个方式实现的，见 k8s.io/apimachinery/pkg/api/meta/meta.go： Accessor
}

// CacheableObject allows an object to cache its different serializations
// to avoid performing the same serialization multiple times.
type CacheableObject interface {
	// CacheEncode writes an object to a stream. The <encode> function will
	// be used in case of cache miss. The <encode> function takes ownership
	// of the object.
	// If CacheableObject is a wrapper, then deep-copy of the wrapped object
	// should be passed to <encode> function.
	// CacheEncode assumes that for two different calls with the same <id>,
	// <encode> function will also be the same.
	CacheEncode(id Identifier, encode func(Object, io.Writer) error, w io.Writer) error
	// GetObject returns a deep-copy of an object to be encoded - the caller of
	// GetObject() is the owner of returned object. The reason for making a copy
	// is to avoid bugs, where caller modifies the object and forgets to copy it,
	// thus modifying the object for everyone.
	// The object returned by GetObject should be the same as the one that is supposed
	// to be passed to <encode> function in CacheEncode method.
	// If CacheableObject is a wrapper, the copy of wrapped object should be returned.
	GetObject() Object
}

// 只能进行 json 的编解码
// Unstructured objects store values as map[string]interface{}, with only values that can be serialized
// to JSON allowed.
type Unstructured interface {
	Object
	// NewEmptyInstance returns a new instance of the concrete type containing only kind/apiVersion and no other data.
	// This should be called instead of reflect.New() for unstructured types because the go type alone does not preserve kind/apiVersion info.
	NewEmptyInstance() Unstructured
	// UnstructuredContent returns a non-nil map with this object's contents. Values may be
	// []interface{}, map[string]interface{}, or any primitive type. Contents are typically serialized to
	// and from JSON. SetUnstructuredContent should be used to mutate the contents.
	UnstructuredContent() map[string]interface{}
	// SetUnstructuredContent updates the object content to match the provided map.
	SetUnstructuredContent(map[string]interface{})
	// IsList returns true if this type is a list or matches the list convention - has an array called "items".
	IsList() bool
	// EachListItem should pass a single item out of the list as an Object to the provided function. Any
	// error should terminate the iteration. If IsList() returns false, this method should return an error
	// instead of calling the provided function.
	EachListItem(func(Object) error) error
}
