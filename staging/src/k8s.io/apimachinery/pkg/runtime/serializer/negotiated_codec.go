/*
Copyright 2016 The Kubernetes Authors.

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
	"k8s.io/apimachinery/pkg/runtime"
)

// TODO: We should split negotiated serializers that we can change versions on from those we can change
// serialization formats on
type negotiatedSerializerWrapper struct {
	info runtime.SerializerInfo
}

func NegotiatedSerializerWrapper(info runtime.SerializerInfo) runtime.NegotiatedSerializer {
	return &negotiatedSerializerWrapper{info}
}

func (n *negotiatedSerializerWrapper) SupportedMediaTypes() []runtime.SerializerInfo {
	return []runtime.SerializerInfo{n.info}
}

// TODO：为什么这里直接返回传入的参数 encoder？
// A：这是统一的接口，所有的 codec-factory 使用的时候应该是需要将自己的 SupportedMediaTypes 传入 EncoderForVersion 中的。
func (n *negotiatedSerializerWrapper) EncoderForVersion(e runtime.Encoder, _ runtime.GroupVersioner) runtime.Encoder {
	return e
}

func (n *negotiatedSerializerWrapper) DecoderToVersion(d runtime.Decoder, _gv runtime.GroupVersioner) runtime.Decoder {
	return d
}
