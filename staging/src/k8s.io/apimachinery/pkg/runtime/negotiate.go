/*
Copyright 2019 The Kubernetes Authors.

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
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// NegotiateError is returned when a ClientNegotiator is unable to locate
// a serializer for the requested operation.
type NegotiateError struct {
	ContentType string
	Stream      bool
}

func (e NegotiateError) Error() string {
	if e.Stream {
		return fmt.Sprintf("no stream serializers registered for %s", e.ContentType)
	}
	return fmt.Sprintf("no serializers registered for %s", e.ContentType)
}

// 进一步封装，判断某个 mediatype 是否可以解析
type clientNegotiator struct {
	// codec 编解码器，生成某个gv的编解码
	serializer     NegotiatedSerializer
	// 编解码的版本
	encode, decode GroupVersioner
}

func (n *clientNegotiator) Encoder(contentType string, params map[string]string) (Encoder, error) {
	// TODO: `pretty=1` is handled in NegotiateOutputMediaType, consider moving it to this method
	// if client negotiators truly need to use it
	mediaTypes := n.serializer.SupportedMediaTypes()
	info, ok := SerializerInfoForMediaType(mediaTypes, contentType)
	if !ok {
		if len(contentType) != 0 || len(mediaTypes) == 0 {
			return nil, NegotiateError{ContentType: contentType}
		}
		info = mediaTypes[0]
	}
	return n.serializer.EncoderForVersion(info.Serializer, n.encode), nil
}

func (n *clientNegotiator) Decoder(contentType string, params map[string]string) (Decoder, error) {
	mediaTypes := n.serializer.SupportedMediaTypes()
	info, ok := SerializerInfoForMediaType(mediaTypes, contentType)
	if !ok {
		if len(contentType) != 0 || len(mediaTypes) == 0 {
			return nil, NegotiateError{ContentType: contentType}
		}
		info = mediaTypes[0]
	}
	return n.serializer.DecoderToVersion(info.Serializer, n.decode), nil
}

func (n *clientNegotiator) StreamDecoder(contentType string, params map[string]string) (Decoder, Serializer, Framer, error) {
	mediaTypes := n.serializer.SupportedMediaTypes()
	info, ok := SerializerInfoForMediaType(mediaTypes, contentType)
	if !ok {
		if len(contentType) != 0 || len(mediaTypes) == 0 {
			return nil, nil, nil, NegotiateError{ContentType: contentType, Stream: true}
		}
		info = mediaTypes[0]
	}
	if info.StreamSerializer == nil {
		return nil, nil, nil, NegotiateError{ContentType: info.MediaType, Stream: true}
	}
	return n.serializer.DecoderToVersion(info.Serializer, n.decode), info.StreamSerializer.Serializer, info.StreamSerializer.Framer, nil
}

// 根据 content type 搜索合适的 encoder、decoder、stream decoder。不会执行任何的转换，但是会将 obj encode 为期望的 gvk。
// 该方法在创建 client 的时候使用
// 传入的参数 NegotiatedSerializer 表示 code-factory
// NewClientNegotiator will attempt to retrieve the appropriate encoder, decoder, or
// stream decoder for a given content type. Does not perform any conversion, but will
// encode the object to the desired group, version, and kind. Use when creating a client.
func NewClientNegotiator(serializer NegotiatedSerializer, gv schema.GroupVersion) ClientNegotiator {
	return &clientNegotiator{
		serializer: serializer,
		encode:     gv,
		// TODO：为什么这里不用初始化 decode 字段？
	}
}

type simpleNegotiatedSerializer struct {
	info SerializerInfo
}

func NewSimpleNegotiatedSerializer(info SerializerInfo) NegotiatedSerializer {
	return &simpleNegotiatedSerializer{info: info}
}

func (n *simpleNegotiatedSerializer) SupportedMediaTypes() []SerializerInfo {
	return []SerializerInfo{n.info}
}

func (n *simpleNegotiatedSerializer) EncoderForVersion(e Encoder, _ GroupVersioner) Encoder {
	return e
}

func (n *simpleNegotiatedSerializer) DecoderToVersion(d Decoder, _gv GroupVersioner) Decoder {
	return d
}
