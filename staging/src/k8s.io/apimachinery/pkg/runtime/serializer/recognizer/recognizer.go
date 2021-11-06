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

package recognizer

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)


// RecognizingDecoder是具有辨识能力的解码器，即判断数据是否能够由自己解析
// 对比 runtime.Decoder，多了 RecognizesData，用于判断序列化格式自己能够解析
type RecognizingDecoder interface {
	// 继承了解码器，很好理解，因为本身就是解码器。
	runtime.Decoder
	// 从序列化数据中提取一定量(也可以是全量)的数据(peek)，根据这部分数据判断序列化数据是否属于该解码器。
	// 举个栗子，如果是json解码器，RecognizesData()就是判断peek是不是json格式数据。
	// 如果辨识成功返回ok为true，如果提供的数据不足以做出决定则返回unknown为true。
	// 首先需要注意的是，在1.20版本，该接口的定义为RecognizesData(peek io.Reader) (ok, unknown bool, err error)。
	// 因为io.Reader.Read()可能会返回错误，导致该接口也可能返回错误，到了1.21版本改成[]byte就不会返回错误了。
	// 所以该接口返回的可能性如下：
	// 1. ok=false, unknown=true: peek提供的数据不足以做出决定，什么情况会返回unknown后续章节会给出例子
	// 2. ok=true, unknown=false: 辨识成功，序列化数据属于该解码器，比如json解码器辨识json数据
	// 3. ok&unknown=false: 辨识失败，序列化数据不属于该解码器，比如json解码器辨识yaml或protobuf数据
	// RecognizesData should return true if the input provided in the provided reader
	// belongs to this decoder, or an error if the data could not be read or is ambiguous.
	// Unknown is true if the data could not be determined to match the decoder type.
	// Decoders should assume that they can read as much of peek as they need (as the caller
	// provides) and may return unknown if the data provided is not sufficient to make a
	// a determination. When peek returns EOF that may mean the end of the input or the
	// end of buffered input - recognizers should return the best guess at that time.
	RecognizesData(peek []byte) (ok, unknown bool, err error)
}


// decoder毕竟是包内的私有类型，如果需要使用它必须通过包内的公有函数构造。
// NewDecoder()是decoder的构造函数，传入各种数据格式的解码器。
// NewDecoder creates a decoder that will attempt multiple decoders in an order defined
// by:
//
// 1. The decoder implements RecognizingDecoder and identifies the data
// 2. All other decoders, and any decoder that returned true for unknown.
//
// The order passed to the constructor is preserved within those priorities.
func NewDecoder(decoders ...runtime.Decoder) runtime.Decoder {
	return &decoder{
		decoders: decoders,
	}
}


// decoder实现了RecognizingDecoder。
type decoder struct {
	// 管理了若干解码器，为什么是[]runtime.Decoder，而不是[]RecognizingDecoder。
	// json，yaml，protobuf的解码器不都实现了RecognizingDecoder么？
	// 笔者猜测这是为扩展考虑的，未来如果增加一种解码器，但是又没有实现辨识接口怎么办？
	// 那么问题来，没有实现辨识接口的解码器还有必要被decoder管理么？
	// 答案是有必要，在非常极端的情况下decoder会尝试让所有解码器都解码一次，只要有一个成功就行，后面会看到代码实现。
	decoders []runtime.Decoder
}

//判断 decoder 是否实现了 RecognizingDecoder 的所有接口
var _ RecognizingDecoder = &decoder{}

// ok
// RecognizesData()实现了RecognizingDecoder.RecognizesData()接口。
// 因为decoder结构体中包含了[]runtime.Decoder成员变量(decoders)，为了区分decoder和decoder.decoders[i]，
// 后续的注释中'decoder'代表的就是decoder结构体，而'解码器'代表的是decoder.decoders[i]。
func (d *decoder) RecognizesData(data []byte) (bool, bool, error) {
	var (
		lastErr    error
		anyUnknown bool
	)
	for _, r := range d.decoders {
		// 将解码器划分为[]RecognizingDecoder和[]runtime.Decoder两个子集，其中只有[]RecognizingDecoder具备辨识能力。
		switch t := r.(type) {
		// 只用decoder.decoders中的[]RecognizingDecoder子集辨识数据格式
		case RecognizingDecoder:
			// 让每个解码器都辨识一下序列化数据
			ok, unknown, err := t.RecognizesData(data)
			if err != nil {
				// 如果解码器辨识出错，则只需要记录最后一个错误就可以了，因为所有解码器报错都应该是一样的。
				// 前面已经提到了，因为历史原因，io.Reader读取数据可能返回错误，当前版本已经不会返回错误了。
				lastErr = err
				continue
			}
			// 只要有任何解码器返回数据不足以做出决定，那么就要返回unknown，除非有任何解码器辨识成功。
			// 这个逻辑应该比较简单：当有一些解码器返回unknown，其他都返回无法辨识的时候，对于decoder而言就是返回unknown。
			// 因为有些解码器只是不知道数据格式是不是属于自己，并不等于辨识失败，是存在可能的
			anyUnknown = anyUnknown || unknown
			if !ok {
				continue
			}
			// 只要有任何一个解码器返回辨识成功，那么就可以直接返辨识成功了
			return true, false, nil
		}
	}
	// 所有的解码器都没有辨识成功，那么如果有任何解码器返回unknown就返回unknown
	return false, anyUnknown, lastErr
}

// ok
// Decode()实现了runtime.Decoder.Decode()接口
// 不挑选数据格式（json、protobuf、yaml）
func (d *decoder) Decode(data []byte, gvk *schema.GroupVersionKind, into runtime.Object) (runtime.Object, *schema.GroupVersionKind, error) {
	var (
		lastErr error
		skipped []runtime.Decoder
	)

	// 遍历所有的解码器
	// try recognizers, record any decoders we need to give a chance later
	for _, r := range d.decoders {
		// 找到[]RecognizingDecoder子集，与RecognizesData()类似。
		switch t := r.(type) {
		case RecognizingDecoder:
			// 这个过程在decoder.RecognizesData()已经注释过了，功能是一样的
			ok, unknown, err := t.RecognizesData(data)
			if err != nil {
				lastErr = err
				continue
			}
			// 把返回unknown的解码器都汇总到skipped中，表示这些先略过，因为他们不知道数据格式是否属于自己。
			if unknown {
				skipped = append(skipped, t)
				continue
			}
			// 解码器明确返回数据不属于解码器，辨识失败，忽略该解码器。需要注意是“忽略”，不是“略过”。
			// “略过”的解码器未来可能还会有用，而忽略的解码器是不会再用的。
			if !ok {
				continue
			}
			// 如果解码器辨识成功，则用该解码器解码
			return r.Decode(data, gvk, into)
		// []runtime.Decoder子集也放入skipped中，他们不具备辨识数据的能力，与unknown本质是一样的
		default:
			skipped = append(skipped, t)
		}
	}

	// 如果没有任何解码器辨识数据成功，那就用[]runtime.Decoder子集和返回unknown的[]RecognizingDecoder子集逐一解码。
	// 这是一个非常简单暴力的做法，但是也没什么好办法。但是不用过分担心，绝大部分情况是可辨识成功的。
	// 也就是说，只有非常极端的情况才会执行这里的代码。
	// try recognizers that returned unknown or didn't recognize their data
	for _, r := range skipped {
		out, actual, err := r.Decode(data, gvk, into)
		if err != nil {
			lastErr = err
			continue
		}
		return out, actual, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no serialization format matched the provided data")
	}
	return nil, nil, lastErr
}
