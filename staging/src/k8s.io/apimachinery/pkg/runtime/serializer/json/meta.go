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

package json

import (
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// MetaFactory is used to store and retrieve the version and kind
// information for JSON objects in a serializer.
type MetaFactory interface {
	// Interpret should return the version and kind of the wire-format of
	// the object.
	// 解析json数据中的元数据字段，返回GVK(Group/Version/Kind)。
	// 如果MetaFactory就这么一个接口函数，笔者认为叫解释器或者解析器更加合理。
	Interpret(data []byte) (*schema.GroupVersionKind, error)
}


// SimpleMetaFactory是MetaFactory的一种实现，用于检索在json中由"apiVersion"和"kind"字段标识的对象的类型和版本。
// unstructured 那边会使用这个：staging/src/k8s.io/apimachinery/pkg/apis/meta/v1/unstructured/unstructuredscheme/scheme.go
// DefaultMetaFactory is a default factory for versioning objects in JSON. The object
// in memory and in the default JSON serialization will use the "kind" and "apiVersion"
// fields.
var DefaultMetaFactory = SimpleMetaFactory{}

// SimpleMetaFactory provides default methods for retrieving the type and version of objects
// that are identified with an "apiVersion" and "kind" fields in their JSON
// serialization. It may be parameterized with the names of the fields in memory, or an
// optional list of base structs to search for those fields in memory.
type SimpleMetaFactory struct {
}

// Interpret will return the APIVersion and Kind of the JSON wire-format
// encoding of an object, or an error.
func (SimpleMetaFactory) Interpret(data []byte) (*schema.GroupVersionKind, error) {
	// 定义一种只有apiVersion和kind两个字段的匿名类型
	findKind := struct {
		// +optional
		APIVersion string `json:"apiVersion,omitempty"`
		// +optional
		Kind string `json:"kind,omitempty"`
	}{}
	// 只解析json中apiVersion和kind字段，这个玩法有点意思，但是笔者认为这个方法有点简单粗暴。
	// 读者可以尝试阅读json.Unmarshal()，该函数会遍历整个json，开销不小，其实必要性不强，因为只需要apiVersion和kind字段。
	// 试想一下，如果每次反序列化一个API对象都要有一次Interpret()和Decode()，它的开销相当于做了两次反序列化。
	if err := json.Unmarshal(data, &findKind); err != nil {
		return nil, fmt.Errorf("couldn't get version/kind; json parse error: %v", err)
	}
	// 将apiVersion解析为Group和Version
	gv, err := schema.ParseGroupVersion(findKind.APIVersion)
	if err != nil {
		return nil, err
	}
	// 返回API对象的GVK
	return &schema.GroupVersionKind{Group: gv.Group, Version: gv.Version, Kind: findKind.Kind}, nil
}
