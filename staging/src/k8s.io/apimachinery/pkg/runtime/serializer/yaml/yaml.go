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

package yaml

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
)


// yamlSerializer实现了runtime.Serializer()。
// 没有定义Serializer类型，而是一个包内的私有类型yamlSerializer，如果需要使用这个类型必须通过包内的公有接口创建。
// yamlSerializer converts YAML passed to the Decoder methods to JSON.
type yamlSerializer struct {
	// the nested serializer
	runtime.Serializer
}

// yamlSerializer implements Serializer
var _ runtime.Serializer = yamlSerializer{}

// NewDecodingSerializer()向支持json的Serializer添加yaml解码支持。
// 也就是说yamlSerializer编码json，解码yaml，当然从接口名字看，调用这个接口估计只需要用解码能力吧。
// 好在笔者检索了一下源码，yamlSerializer以及NewDecodingSerializer()没有引用的地方，应该是历史遗留的代码。
// NewDecodingSerializer adds YAML decoding support to a serializer that supports JSON.
func NewDecodingSerializer(jsonSerializer runtime.Serializer) runtime.Serializer {
	return &yamlSerializer{jsonSerializer}
}

func (c yamlSerializer) Decode(data []byte, gvk *schema.GroupVersionKind, into runtime.Object) (runtime.Object, *schema.GroupVersionKind, error) {
	// yaml->json
	out, err := yaml.ToJSON(data)
	if err != nil {
		return nil, nil, err
	}
	// 反序列化jso
	data = out
	return c.Serializer.Decode(data, gvk, into)
}
