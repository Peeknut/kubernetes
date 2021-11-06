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
	"io"
	"strconv"
	"unsafe"

	jsoniter "github.com/json-iterator/go"
	"github.com/modern-go/reflect2"
	"sigs.k8s.io/yaml"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/recognizer"
	"k8s.io/apimachinery/pkg/util/framer"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/klog/v2"
)

// NewSerializer creates a JSON serializer that handles encoding versioned objects into the proper JSON form. If typer
// is not nil, the object has the group, version, and kind fields set.
// Deprecated: use NewSerializerWithOptions instead.
func NewSerializer(meta MetaFactory, creater runtime.ObjectCreater, typer runtime.ObjectTyper, pretty bool) *Serializer {
	return NewSerializerWithOptions(meta, creater, typer, SerializerOptions{false, pretty, false})
}

// NewYAMLSerializer creates a YAML serializer that handles encoding versioned objects into the proper YAML form. If typer
// is not nil, the object has the group, version, and kind fields set. This serializer supports only the subset of YAML that
// matches JSON, and will error if constructs are used that do not serialize to JSON.
// Deprecated: use NewSerializerWithOptions instead.
func NewYAMLSerializer(meta MetaFactory, creater runtime.ObjectCreater, typer runtime.ObjectTyper) *Serializer {
	return NewSerializerWithOptions(meta, creater, typer, SerializerOptions{true, false, false})
}

// 根据配置，创建 json/yaml serializer
// NewSerializerWithOptions creates a JSON/YAML serializer that handles encoding versioned objects into the proper JSON/YAML
// form. If typer is not nil, the object has the group, version, and kind fields set. Options are copied into the Serializer
// and are immutable.
func NewSerializerWithOptions(meta MetaFactory, creater runtime.ObjectCreater, typer runtime.ObjectTyper, options SerializerOptions) *Serializer {
	return &Serializer{
		meta:       meta,
		creater:    creater,
		typer:      typer,
		options:    options,
		identifier: identifier(options),
	}
}


// identifier()根据给定的选项计算编码器的标识符
// identifier computes Identifier of Encoder based on the given options.
func identifier(options SerializerOptions) runtime.Identifier {
	// 编码器的唯一标识符是一个map[string]string，不同属性的组合形成更了唯一性。
	result := map[string]string{
		// 名字是json，表明是json编码器。
		"name":   "json",
		// 输出格式为yaml或json
		"yaml":   strconv.FormatBool(options.Yaml),
		// 是否为pretty模式
		"pretty": strconv.FormatBool(options.Pretty),
	}
	// 序列化成json，生成最终的标识符，json序列化是标签的一种字符串形式。
	identifier, err := json.Marshal(result)
	if err != nil {
		klog.Fatalf("Failed marshaling identifier for json Serializer: %v", err)
	}
	// 也就是说，只要yaml和pretty选项相同的任意两个json.Serializer，任何时候编码同一个API对象输出一定是相同的。
	// 所以当API对象被多个编码器多次编码时，以编码器标识符为键利用缓冲避免重复编码
	return runtime.Identifier(identifier)
}

// SerializerOptions holds the options which are used to configure a JSON/YAML serializer.
// example:
// (1) To configure a JSON serializer, set `Yaml` to `false`.
// (2) To configure a YAML serializer, set `Yaml` to `true`.
// (3) To configure a strict serializer that can return strictDecodingError, set `Strict` to `true`.
type SerializerOptions struct {
	// Yaml: configures the Serializer to work with JSON(false) or YAML(true).
	// When `Yaml` is enabled, this serializer only supports the subset of YAML that
	// matches JSON, and will error if constructs are used that do not serialize to JSON.
	// true: 序列化/反序列化yaml；false: 序列化/反序列化json
	// 也就是说，json.Serializer既可以序列化/反序列json，也可以序列化/反序列yaml。
	Yaml bool

	// Pretty: configures a JSON enabled Serializer(`Yaml: false`) to produce human-readable output.
	// This option is silently ignored when `Yaml` is `true`.
	// Pretty选项仅用于Encode接口，输出易于阅读的json数据。当Yaml选项为true时，Pretty选项被忽略，因为yaml本身就易于阅读。
	// 什么是易于阅读的？举个例子就立刻明白了，定义测试类型为：
	// type Test struct {
	//     A int
	//     B string
	// }
	// 则关闭和开启Pretty选项的对比如下：
	// {"A":1,"B":"2"}
	// {
	//   "A": 1,
	//   "B": "2"
	// }
	// 很明显，后者更易于阅读。易于阅读只有人看的时候才有需要，对于机器来说一点价值都没有，所以这个选项使用范围还是比较有限的。
	Pretty bool

	// Strict: configures the Serializer to return strictDecodingError's when duplicate fields are present decoding JSON or YAML.
	// Note that enabling this option is not as performant as the non-strict variant, and should not be used in fast paths.
	// Strict应用于Decode接口，表示严谨的。那什么是严谨的？笔者很难用语言表达，但是以下几种情况是不严谨的：
	// 1. 存在重复字段，比如{"value":1,"value":1};
	// 2. 不存在的字段，比如{"unknown": 1}，而目标API对象中不存在Unknown属性;
	// 3. 未打标签字段，比如{"Other":"test"}，虽然目标API对象中有Other字段，但是没有打`json:"Other"`标签
	// Strict选项可以理解为增加了很多校验，请注意，启用此选项的性能下降非常严重，因此不应在性能敏感的场景中使用。
	// 那什么场景需要用到Strict选项？比如Kubernetes各个服务的配置API，对性能要求不高，但需要严格的校验。
	Strict bool
}

// Serializer handles encoding versioned objects into the proper JSON form
type Serializer struct {
	// MetaFactory从json数据中提取GVK(Group/Version/Kind)，下面有MetaFactory注释。
	// MetaFactory很有用，解码时如果不提供默认的GVK和API对象指针，就要靠MetaFactory提取GVK了。
	// 当然，即便提供了供默认的GVK和API对象指针，提取的GVK的也是非常有用的，详情参看Decode()接口的实现。
	// 用户解码 decoder，即反序列化为 API 对象
	meta    MetaFactory
	// SerializerOptions是Serializer选项，可以看做是配置，下面有注释。
	options SerializerOptions
	// runtime.ObjectCreater根据GVK构造API对象，在反序列化时会用到，其实它就是Schema。
	// runtime.ObjectCreater的定义读者可以自己查看源码，如果对Schema熟悉的读者这都不是事。
	creater runtime.ObjectCreater
	// runtime.ObjectTyper根据API对象返回可能的GVK，也是用在反序列化中，其实它也是Schema。
	// 这个有什么用？runtime.Serializer.Decode()接口注释说的很清楚，在json数据和默认GVK无法提供的类型元数据需要用输出类型补全。
	typer   runtime.ObjectTyper
	// 标识符，Serializer一旦被创建，标识符就不会变了。
	identifier runtime.Identifier
}

// Serializer implements Serializer
var _ runtime.Serializer = &Serializer{}
var _ recognizer.RecognizingDecoder = &Serializer{}

type customNumberExtension struct {
	jsoniter.DummyExtension
}

func (cne *customNumberExtension) CreateDecoder(typ reflect2.Type) jsoniter.ValDecoder {
	if typ.String() == "interface {}" {
		return customNumberDecoder{}
	}
	return nil
}

type customNumberDecoder struct {
}

func (customNumberDecoder) Decode(ptr unsafe.Pointer, iter *jsoniter.Iterator) {
	switch iter.WhatIsNext() {
	case jsoniter.NumberValue:
		var number jsoniter.Number
		iter.ReadVal(&number)
		i64, err := strconv.ParseInt(string(number), 10, 64)
		if err == nil {
			*(*interface{})(ptr) = i64
			return
		}
		f64, err := strconv.ParseFloat(string(number), 64)
		if err == nil {
			*(*interface{})(ptr) = f64
			return
		}
		iter.ReportError("DecodeNumber", err.Error())
	default:
		*(*interface{})(ptr) = iter.Read()
	}
}

// CaseSensitiveJSONIterator returns a jsoniterator API that's configured to be
// case-sensitive when unmarshalling, and otherwise compatible with
// the encoding/json standard library.
func CaseSensitiveJSONIterator() jsoniter.API {
	config := jsoniter.Config{
		EscapeHTML:             true,
		SortMapKeys:            true,
		ValidateJsonRawMessage: true,
		CaseSensitive:          true,
	}.Froze()
	// Force jsoniter to decode number to interface{} via int64/float64, if possible.
	config.RegisterExtension(&customNumberExtension{})
	return config
}

// StrictCaseSensitiveJSONIterator returns a jsoniterator API that's configured to be
// case-sensitive, but also disallows unknown fields when unmarshalling. It is compatible with
// the encoding/json standard library.
func StrictCaseSensitiveJSONIterator() jsoniter.API {
	config := jsoniter.Config{
		EscapeHTML:             true,
		SortMapKeys:            true,
		ValidateJsonRawMessage: true,
		CaseSensitive:          true,
		DisallowUnknownFields:  true,
	}.Froze()
	// Force jsoniter to decode number to interface{} via int64/float64, if possible.
	config.RegisterExtension(&customNumberExtension{})
	return config
}

// Private copies of jsoniter to try to shield against possible mutations
// from outside. Still does not protect from package level jsoniter.Register*() functions - someone calling them
// in some other library will mess with every usage of the jsoniter library in the whole program.
// See https://github.com/json-iterator/go/issues/265
var caseSensitiveJSONIterator = CaseSensitiveJSONIterator()
var strictCaseSensitiveJSONIterator = StrictCaseSensitiveJSONIterator()


// gvkWithDefaults()利用defaultGVK补全actual中未设置的字段。
// 需要注意的是，参数'defaultGVK'只是一次调用相对于actual的默认GVK，不是Serializer.Decode()的默认GVK。
// gvkWithDefaults returns group kind and version defaulting from provided default
func gvkWithDefaults(actual, defaultGVK schema.GroupVersionKind) schema.GroupVersionKind {
	// actual如果没有设置Kind则用默认的Kind补全
	if len(actual.Kind) == 0 {
		actual.Kind = defaultGVK.Kind
	}
	// 如果Group和Version都没有设置，则用默认的Group和Version补全。
	// 为什么必须是都没有设置？缺少Version或者Group有什么问题么？下面的代码给出了答案。
	if len(actual.Version) == 0 && len(actual.Group) == 0 {
		actual.Group = defaultGVK.Group
		actual.Version = defaultGVK.Version
	}
	// 如果Version未设置，则用默认的Version补全，但是前提是Group与默认Group相同。
	// 因为Group不同的API即便Kind/Version相同可能是两个完全不同的类型，比如自定义资源(CRD)
	if len(actual.Version) == 0 && actual.Group == defaultGVK.Group {
		actual.Version = defaultGVK.Version
	}
	// 如果Group未设置而Version与默认的Version相同，为什么不用默认的Group补全？
	// 前面已经解释过了，应该不用再重复了
	return actual
}

// TODO：怎么保证输入的参数 gvk、into 与 data 中获取的参数的 gvk 信息有一定的相关性？还是不需要有相关性，因为会在 scheme 中检查对应的 gvk 信息？
// Decode实现了Decoder.Decode()，尝试从数据中提取的API类型(GVK)，应用提供的默认GVK，然后将数据加载到所需类型或提供的'into'匹配的对象中：
// 1. 如果into为*runtime.Unknown，则将提取原始数据，并且不执行解码；
// 2. 如果into的类型没有在Schema注册，则使用json.Unmarshal()直接反序列化到'into'指向的对象中；
// 3. 如果'into'不为空且原始数据GVK不全，则'into'的类型(GVK)将用于补全GVK;
// 4. 如果'into'为空或数据中的GVK与'into'的GVK不同，它将使用ObjectCreater.New(gvk)生成一个新对象;
// 成功或大部分错误都会返回GVK，GVK的计算优先级为originalData > default gvk > into.
// Decode attempts to convert the provided data into YAML or JSON, extract the stored schema kind, apply the provided default gvk, and then
// load that data into an object matching the desired schema kind or the provided into.
// If into is *runtime.Unknown, the raw data will be extracted and no decoding will be performed.
// If into is not registered with the typer, then the object will be straight decoded using normal JSON/YAML unmarshalling.
// If into is provided and the original data is not fully qualified with kind/version/group, the type of the into will be used to alter the returned gvk.
// If into is nil or data's gvk different from into's gvk, it will generate a new Object with ObjectCreater.New(gvk)
// On success or most errors, the method will return the calculated schema kind.
// The gvk calculate priority will be originalData > default gvk > into
func (s *Serializer) Decode(originalData []byte, gvk *schema.GroupVersionKind, into runtime.Object) (runtime.Object, *schema.GroupVersionKind, error) {
	//TODO： Q：传入的参数 gvk 是用户传入的吗？（知道的是 gvk 不是从 into 中获取的）
	data := originalData
	// 如果配置选项为yaml，则将yaml格式转为json格式，是不是有一种感觉：“卧了个槽”！所谓可支持json和yaml，就是先将yaml转为json！
	// 那我感觉我可以支持所有格式，都转为json就完了呗！其实不然，yaml的使用场景都是需要和人交互的地方，所以对于效率要求不高(qps低)。
	// 那么实现简单易于维护更重要，所以这种实现并没有什么毛病。
	if s.options.Yaml {
		// yaml 转 json
		altered, err := yaml.YAMLToJSON(data)
		if err != nil {
			return nil, nil, err
		}
		data = altered
	}
	// 此时data是json了，以后就不用再考虑yaml选项了

	// 解析类型元数据，就是从 json 数据中获取 gvk 信息
	actual, err := s.meta.Interpret(data)
	if err != nil {
		return nil, nil, err
	}

	// 解析类型元数据大部分情况是正确的，除非不是json或者apiVersion格式不对。
	// 但是GVK三元组可能有所缺失，比如只有Kind，Group/Version，其他字段就用默认的GVK补全。
	// 这也体现出了原始数据中的GVK的优先级最高，其次是默认的GVK。gvkWithDefaults()函数下面有注释。
	if gvk != nil {
		*actual = gvkWithDefaults(*actual, *gvk)
	}

	// 如果'into'是*runtime.Unknown类型，需要返回*runtime.Unknown类型的对象。
	// 需要注意的是，此处复用了'into'指向的对象，因为返回的类型与'into'指向的类型完全匹配。
	if unk, ok := into.(*runtime.Unknown); ok && unk != nil {
		unk.Raw = originalData
		// 输出数据格式为JSON，那么问题来了，不是'data'才能保证是json么？如s.options.Yaml == true，originalData是yaml格式才对。
		// 所以笔者有必要提一个issue，看看官方怎么解决，如此明显的一个问题为什么没有暴露出来？
		// 笔者猜测：runtime.Unknown只在内部使用，而内部只用json格式，所以自然不会暴露出来。
		unk.ContentType = runtime.ContentTypeJSON
		unk.GetObjectKind().SetGroupVersionKind(*actual)
		return unk, actual, nil
	}

	// 'into'不为空，通过into类型提取GVK，这样在原始数据中的GVK和默认GVK都没有的字段用into的GVK补全。
	if into != nil {
		// 判断'into'是否为runtime.Unstructured类型
		_, isUnstructured := into.(runtime.Unstructured)
		// 获取'into'的GVK，需要注意的是返回的是一组GVK，types[0]是推荐的GVK。
		types, _, err := s.typer.ObjectKinds(into)
		switch {
		// 'into'的类型如果没有被注册或者为runtime.Unstructured类型，则直接反序列成'into'指向的对象。
		// 没有被注册的类型自然无法构造对象，而非结构体等同于map[string]interface{}，不可能是API对象(因为API对象必须是结构体)。
		// 所以这两种情况直接反序列化到'into'对象就可以了，此时与json.Unmarshal()没什么区别。
		case runtime.IsNotRegisteredError(err), isUnstructured:
			if err := caseSensitiveJSONIterator.Unmarshal(data, into); err != nil {
				return nil, actual, err
			}
			return into, actual, nil
		// 获取'into'类型出错，多半是因为不是指针
		case err != nil:
			return nil, actual, err
		// 用'into'的GVK补全未设置的GVK，所以GVK的优先级:originalData > default gvk > into
		default:
			*actual = gvkWithDefaults(*actual, types[0])
		}
	}

	// 那么问题来了，为什么不判断Group？Group为""表示"core"，比如我们写ymal的时候Kind为Pod，apiVersion是v1，并没有设置Group。
	if len(actual.Kind) == 0 {
		return nil, actual, runtime.NewMissingKindErr(string(originalData))
	}
	if len(actual.Version) == 0 {
		return nil, actual, runtime.NewMissingVersionErr(string(originalData))
	}

	// 从函数名字可以看出复用'into'或者重新构造对象，复用的原则是：如果'into'注册的一组GVK有任何一个与*actual相同，则复用'into'。
	// use the target if necessary
	obj, err := runtime.UseOrCreateObject(s.typer, s.creater, *actual, into)
	if err != nil {
		return nil, actual, err
	}

	// 反序列化对象，caseSensitiveJSONIterator暂且不用关心，此处可以理解为json.Unmarshal()。
	// 当然，读者非要知道个究竟，可以看看代码，笔者此处不注释。
	if err := caseSensitiveJSONIterator.Unmarshal(data, obj); err != nil {
		return nil, actual, err
	}

	// 如果是非strict模式，可以直接返回了。其实到此为止就可以了，后面是针对strict模式的代码，是否了解并不重要
	// If the deserializer is non-strict, return successfully here.
	if !s.options.Strict {
		return obj, actual, nil
	}

	// 笔者第一眼看到下面的以为看错了，但是擦了擦懵逼的双眼，发现就是YAMLToJSON。如果原始数据是json不会有问题么？
	// 笔者查看了一下yaml.YAMLToJSONStrict()函数注释：由于JSON是YAML的子集，因此通过此方法传递JSON应该是没有任何操作的。
	// 除非存在重复的字段，会解析出错。所以此处就是用来检测是否有重复字段的，当然，如果是yaml格式顺便转成了json。
	// 感兴趣的读者可以阅读源码，笔者只要知道它的功能就行了，就不“深究”了
	// In strict mode pass the data trough the YAMLToJSONStrict converter.
	// This is done to catch duplicate fields regardless of encoding (JSON or YAML). For JSON data,
	// the output would equal the input, unless there is a parsing error such as duplicate fields.
	// As we know this was successful in the non-strict case, the only error that may be returned here
	// is because of the newly-added strictness. hence we know we can return the typed strictDecoderError
	// the actual error is that the object contains duplicate fields.
	altered, err := yaml.YAMLToJSONStrict(originalData)
	if err != nil {
		return nil, actual, runtime.NewStrictDecodingError(err.Error(), string(originalData))
	}
	// As performance is not an issue for now for the strict deserializer (one has regardless to do
	// the unmarshal twice), we take the sanitized, altered data that is guaranteed to have no duplicated
	// fields, and unmarshal this into a copy of the already-populated obj. Any error that occurs here is
	// due to that a matching field doesn't exist in the object. hence we can return a typed strictDecoderError,
	// the actual error is that the object contains unknown field.
	// 接下来会因为未知的字段报错，比如对象未定义的字段，未打标签的字段等。
	// 此处使用DeepCopyObject()等同于新构造了一个对象，而这个对象其实又没什么用，仅作为一个临时的变量使用。
	strictObj := obj.DeepCopyObject()
	if err := strictCaseSensitiveJSONIterator.Unmarshal(altered, strictObj); err != nil {
		return nil, actual, runtime.NewStrictDecodingError(err.Error(), string(originalData))
	}
	// 返回反序列化的对象、GVK，所谓的strict模式无非是再做了一次转换和反序列化来校验数据的正确性，结果直接丢弃。
	// 所以说strict没有必要不用开启，除非你真正理解他的作用并且能够承受带来的后果。
	// Always return the same object as the non-strict serializer to avoid any deviations.
	return obj, actual, nil
}

// Encode serializes the provided object to the given writer.
func (s *Serializer) Encode(obj runtime.Object, w io.Writer) error {
	// CacheableObject允许对象缓存其不同的序列化数据，以避免多次执行相同的序列化，这是一种出于效率考虑设计的类型。
	// 因为同一个对象可能会多次序列化json、yaml和protobuf，此时就需要根据编码器的标识符找到对应的序列化数据。
	if co, ok := obj.(runtime.CacheableObject); ok {
		// CacheableObject笔者不再注释了，感兴趣的读者可以自行阅读源码。
		// 其实根据传入的参数也能猜出来具体实现：利用标识符查一次map，如果有就输出，没有就调用一次s.doEncode()。
		return co.CacheEncode(s.Identifier(), s.doEncode, w)
	}
	// 非CacheableObject对象，就执行一次json的序列化
	return s.doEncode(obj, w)
}


// doEncode()类似于于json.Marshal()，只是写入是io.Writer而不是[]byte。
func (s *Serializer) doEncode(obj runtime.Object, w io.Writer) error {
	if s.options.Yaml {
		// 序列化对象为json
		json, err := caseSensitiveJSONIterator.Marshal(obj)
		if err != nil {
			return err
		}
		// json->yaml
		data, err := yaml.JSONToYAML(json)
		if err != nil {
			return err
		}
		// 写入io.Writer。
		_, err = w.Write(data)
		return err
	}

	// 输出易于理解的格式？那么问题来了，为什么输出yaml不需要这个判断？很简单，yaml就是易于理解的
	if s.options.Pretty {
		// 序列化对象为json
		data, err := caseSensitiveJSONIterator.MarshalIndent(obj, "", "  ")
		if err != nil {
			return err
		}
		// 写入io.Writer。
		_, err = w.Write(data)
		return err
	}
	// 非pretty模式，用我们最常用的json序列化方法，无非我们最常用方法是json.Marshal()。
	// 如果需要写入io.Writer，下面的代码是标准写法。
	encoder := json.NewEncoder(w)
	return encoder.Encode(obj)
}

// Identifier implements runtime.Encoder interface.
func (s *Serializer) Identifier() runtime.Identifier {
	return s.identifier
}


// RecognizesData()实现了RecognizingDecoder.RecognizesData()接口
// RecognizesData implements the RecognizingDecoder interface.
func (s *Serializer) RecognizesData(data []byte) (ok, unknown bool, err error) {
	if s.options.Yaml {
		// 如果是yaml选项(即yaml编解码器)，直接返回unknown，这个操作可以啊，不打算争取一下了么？
		// 其实道理很简单，yaml实在是没有什么明显的特征，所以返回unknown，表示不知道是不是yaml格式
		// we could potentially look for '---'
		return false, true, nil
	}
	// 既然无法辨识yaml格式，那json格式总不至于也无法辨识吧，毕竟json有明显的特征"{}"。
	// utilyaml.IsJSONBuffer()的函数实现就是找数据中是否以'{'开头(前面的空格除外)。
	// 这个原理就很简单，因为API类型都是结构体（不存在数组和空指针），所以json数据都是'{...}'格式
	return utilyaml.IsJSONBuffer(data), false, nil
}

// json 框架默认的行为，用换行符分隔单个对象
// Framer is the default JSON framing behavior, with newlines delimiting individual objects.
var Framer = jsonFramer{}

type jsonFramer struct{}

// NewFrameWriter implements stream framing for this serializer
func (jsonFramer) NewFrameWriter(w io.Writer) io.Writer {
	// we can write JSON objects directly to the writer, because they are self-framing
	return w
}

// NewFrameReader implements stream framing for this serializer
func (jsonFramer) NewFrameReader(r io.ReadCloser) io.ReadCloser {
	// we need to extract the JSON chunks of data to pass to Decode()
	return framer.NewJSONFramedReader(r)
}

// YAMLFramer is the default JSON framing behavior, with newlines delimiting individual objects.
var YAMLFramer = yamlFramer{}

type yamlFramer struct{}

// NewFrameWriter implements stream framing for this serializer
func (yamlFramer) NewFrameWriter(w io.Writer) io.Writer {
	return yamlFrameWriter{w}
}

// NewFrameReader implements stream framing for this serializer
func (yamlFramer) NewFrameReader(r io.ReadCloser) io.ReadCloser {
	// extract the YAML document chunks directly
	return utilyaml.NewDocumentDecoder(r)
}

type yamlFrameWriter struct {
	w io.Writer
}

// Write separates each document with the YAML document separator (`---` followed by line
// break). Writers must write well formed YAML documents (include a final line break).
func (w yamlFrameWriter) Write(data []byte) (n int, err error) {
	if _, err := w.w.Write([]byte("---\n")); err != nil {
		return 0, err
	}
	return w.w.Write(data)
}
