/*
Copyright The Kubernetes Authors.

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

// Code generated by informer-gen. DO NOT EDIT.

package informers

import (
	reflect "reflect"
	sync "sync"
	time "time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	schema "k8s.io/apimachinery/pkg/runtime/schema"
	admissionregistration "k8s.io/client-go/informers/admissionregistration"
	apiserverinternal "k8s.io/client-go/informers/apiserverinternal"
	apps "k8s.io/client-go/informers/apps"
	autoscaling "k8s.io/client-go/informers/autoscaling"
	batch "k8s.io/client-go/informers/batch"
	certificates "k8s.io/client-go/informers/certificates"
	coordination "k8s.io/client-go/informers/coordination"
	core "k8s.io/client-go/informers/core"
	discovery "k8s.io/client-go/informers/discovery"
	events "k8s.io/client-go/informers/events"
	extensions "k8s.io/client-go/informers/extensions"
	flowcontrol "k8s.io/client-go/informers/flowcontrol"
	internalinterfaces "k8s.io/client-go/informers/internalinterfaces"
	networking "k8s.io/client-go/informers/networking"
	node "k8s.io/client-go/informers/node"
	policy "k8s.io/client-go/informers/policy"
	rbac "k8s.io/client-go/informers/rbac"
	scheduling "k8s.io/client-go/informers/scheduling"
	storage "k8s.io/client-go/informers/storage"
	kubernetes "k8s.io/client-go/kubernetes"
	cache "k8s.io/client-go/tools/cache"
)

// ok
// 配置 sharedInformerFactory
// SharedInformerOption defines the functional option type for SharedInformerFactory.
type SharedInformerOption func(*sharedInformerFactory) *sharedInformerFactory

// ok
type sharedInformerFactory struct {
	// ok
	// list/watch 请求时，需要与 APIserver 进行连接，连接的认证信息在 client 中
	client           kubernetes.Interface
	// factory 关注的namepace，可以通过WithNamespace Option配置。默认是 all namespaces。
	// informer中的reflector将只会listAndWatch指定namespace的资源
	namespace        string
	tweakListOptions internalinterfaces.TweakListOptionsFunc
	lock             sync.Mutex

	// 用于初始化持有的shareIndexInformer的resyncCheckPeriod和defaultEventHandlerResyncPeriod字段，用于定时的将local store同步到deltaFIFO
	defaultResync    time.Duration
	// 支持针对每一个informer来配置resync时间，通过WithCustomResyncConfig这个Option配置，否则就用指定的defaultResync
	customResync     map[reflect.Type]time.Duration  //自定义resync时间

	// ok
	//针对每种类型资源存储一个informer，informer的类型是ShareIndexInformer
	informers map[reflect.Type]cache.SharedIndexInformer

	// startedInformers is used for tracking which informers have been started.
	// This allows Start() to be called multiple times safely.
	//记录已经启动的informer集合
	startedInformers map[reflect.Type]bool
}

// WithCustomResyncConfig sets a custom resync period for the specified informer types.
func WithCustomResyncConfig(resyncConfig map[v1.Object]time.Duration) SharedInformerOption {
	return func(factory *sharedInformerFactory) *sharedInformerFactory {
		for k, v := range resyncConfig {
			factory.customResync[reflect.TypeOf(k)] = v
		}
		return factory
	}
}

// WithTweakListOptions sets a custom filter on all listers of the configured SharedInformerFactory.
func WithTweakListOptions(tweakListOptions internalinterfaces.TweakListOptionsFunc) SharedInformerOption {
	return func(factory *sharedInformerFactory) *sharedInformerFactory {
		factory.tweakListOptions = tweakListOptions
		return factory
	}
}

// WithNamespace limits the SharedInformerFactory to the specified namespace.
func WithNamespace(namespace string) SharedInformerOption {
	return func(factory *sharedInformerFactory) *sharedInformerFactory {
		factory.namespace = namespace
		return factory
	}
}

// ok
// 这个函数对外使用比较多
// NewSharedInformerFactory constructs a new instance of sharedInformerFactory for all namespaces.
func NewSharedInformerFactory(client kubernetes.Interface, defaultResync time.Duration) SharedInformerFactory {
	return NewSharedInformerFactoryWithOptions(client, defaultResync)
}

// NewFilteredSharedInformerFactory constructs a new instance of sharedInformerFactory.
// Listers obtained via this SharedInformerFactory will be subject to the same filters
// as specified here.
// Deprecated: Please use NewSharedInformerFactoryWithOptions instead
func NewFilteredSharedInformerFactory(client kubernetes.Interface, defaultResync time.Duration, namespace string, tweakListOptions internalinterfaces.TweakListOptionsFunc) SharedInformerFactory {
	return NewSharedInformerFactoryWithOptions(client, defaultResync, WithNamespace(namespace), WithTweakListOptions(tweakListOptions))
}

// ok
// 根据 options 配置 sharedInformerFactory
// NewSharedInformerFactoryWithOptions constructs a new instance of a SharedInformerFactory with additional options.
func NewSharedInformerFactoryWithOptions(client kubernetes.Interface, defaultResync time.Duration, options ...SharedInformerOption) SharedInformerFactory {
	// 初始化的时候，informers 字段是空的，需要后续用户自己加进去。——跟我原来设想的不一样，我以为 factory 中应该存放了所有资源的 informer
	// 用户只需要调用就可以了。
	factory := &sharedInformerFactory{
		client:           client,
		namespace:        v1.NamespaceAll,  // 默认监听所有ns下的指定资源
		defaultResync:    defaultResync,  //30s
		informers:        make(map[reflect.Type]cache.SharedIndexInformer),
		startedInformers: make(map[reflect.Type]bool),
		customResync:     make(map[reflect.Type]time.Duration),
	}

	// Apply all options
	for _, opt := range options {
		factory = opt(factory)
	}

	return factory
}

// ok
// 启动 factory 下所有的 informer
// Start initializes all requested informers.
func (f *sharedInformerFactory) Start(stopCh <-chan struct{}) {
	f.lock.Lock()
	defer f.lock.Unlock()

	for informerType, informer := range f.informers {
		if !f.startedInformers[informerType] {
			//直接起gorouting调用informer的Run方法，并且标记对应的informer已经启动
			// 运行一个 informer，即进行 list/watch
			go informer.Run(stopCh)
			f.startedInformers[informerType] = true
		}
	}
}


// ok
// 等待 informer 的 cache 被同步

// sharedInformerFactory的WaitForCacheSync将会不断调用factory持有的所有informer的HasSynced方法，直到返回true
//
//而informer的HasSynced方法调用的自己持有的controller的HasSynced方法（informer结构持有controller对象，下文会分析informer的结构）
//
//informer中的controller的HasSynced方法则调用的是controller持有的deltaFIFO对象的HasSynced方法
// WaitForCacheSync waits for all started informers' cache were synced.
func (f *sharedInformerFactory) WaitForCacheSync(stopCh <-chan struct{}) map[reflect.Type]bool {
	// 这里会直接执行这个函数
	informers := func() map[reflect.Type]cache.SharedIndexInformer {
		f.lock.Lock()
		defer f.lock.Unlock()

		informers := map[reflect.Type]cache.SharedIndexInformer{}
		for informerType, informer := range f.informers {
			if f.startedInformers[informerType] {
				informers[informerType] = informer
			}
		}
		return informers
	}()

	res := map[reflect.Type]bool{}
	// 等待他们的cache被同步，调用的是informer的HasSynced方法
	for informType, informer := range informers {
		// 这里每次调用 cache.WaitForCacheSync 都会阻塞，直到该 informer.HasSynced 返回 true，才会开始遍历下一个 informer
		res[informType] = cache.WaitForCacheSync(stopCh, informer.HasSynced)
	}
	return res
}

// ok
// 向 factory 添加某个资源的 informer。原来的 factory 中是没有 informer 的，需要用户自己添加。
// 参数：
// 		obj: informer关注的资源如deployment{}
//		newFunc: 一个知道如何创建指定informer的方法，k8s为每一个内置的对象都实现了这个方法（相关代码在 client-go 中），比如创建deployment的ShareIndexInformer的方法
// InternalInformerFor returns the SharedIndexInformer for obj using an internal
// client.
func (f *sharedInformerFactory) InformerFor(obj runtime.Object, newFunc internalinterfaces.NewInformerFunc) cache.SharedIndexInformer {
	f.lock.Lock()
	defer f.lock.Unlock()

	informerType := reflect.TypeOf(obj)
	informer, exists := f.informers[informerType]
	if exists {
		return informer
	}

	resyncPeriod, exists := f.customResync[informerType]
	if !exists {
		resyncPeriod = f.defaultResync
	}

	informer = newFunc(f.client, resyncPeriod)
	f.informers[informerType] = informer

	return informer
}

// SharedInformerFactory provides shared informers for resources in all known
// API group versions.
type SharedInformerFactory interface {
	internalinterfaces.SharedInformerFactory
	ForResource(resource schema.GroupVersionResource) (GenericInformer, error)
	WaitForCacheSync(stopCh <-chan struct{}) map[reflect.Type]bool

	Admissionregistration() admissionregistration.Interface
	Internal() apiserverinternal.Interface
	Apps() apps.Interface
	Autoscaling() autoscaling.Interface
	Batch() batch.Interface
	Certificates() certificates.Interface
	Coordination() coordination.Interface
	Core() core.Interface
	Discovery() discovery.Interface
	Events() events.Interface
	Extensions() extensions.Interface
	Flowcontrol() flowcontrol.Interface
	Networking() networking.Interface
	Node() node.Interface
	Policy() policy.Interface
	Rbac() rbac.Interface
	Scheduling() scheduling.Interface
	Storage() storage.Interface
}

func (f *sharedInformerFactory) Admissionregistration() admissionregistration.Interface {
	return admissionregistration.New(f, f.namespace, f.tweakListOptions)
}

func (f *sharedInformerFactory) Internal() apiserverinternal.Interface {
	return apiserverinternal.New(f, f.namespace, f.tweakListOptions)
}

func (f *sharedInformerFactory) Apps() apps.Interface {
	return apps.New(f, f.namespace, f.tweakListOptions)
}

func (f *sharedInformerFactory) Autoscaling() autoscaling.Interface {
	return autoscaling.New(f, f.namespace, f.tweakListOptions)
}

func (f *sharedInformerFactory) Batch() batch.Interface {
	return batch.New(f, f.namespace, f.tweakListOptions)
}

func (f *sharedInformerFactory) Certificates() certificates.Interface {
	return certificates.New(f, f.namespace, f.tweakListOptions)
}

func (f *sharedInformerFactory) Coordination() coordination.Interface {
	return coordination.New(f, f.namespace, f.tweakListOptions)
}

func (f *sharedInformerFactory) Core() core.Interface {
	return core.New(f, f.namespace, f.tweakListOptions)
}

func (f *sharedInformerFactory) Discovery() discovery.Interface {
	return discovery.New(f, f.namespace, f.tweakListOptions)
}

func (f *sharedInformerFactory) Events() events.Interface {
	return events.New(f, f.namespace, f.tweakListOptions)
}

func (f *sharedInformerFactory) Extensions() extensions.Interface {
	return extensions.New(f, f.namespace, f.tweakListOptions)
}

func (f *sharedInformerFactory) Flowcontrol() flowcontrol.Interface {
	return flowcontrol.New(f, f.namespace, f.tweakListOptions)
}

func (f *sharedInformerFactory) Networking() networking.Interface {
	return networking.New(f, f.namespace, f.tweakListOptions)
}

func (f *sharedInformerFactory) Node() node.Interface {
	return node.New(f, f.namespace, f.tweakListOptions)
}

func (f *sharedInformerFactory) Policy() policy.Interface {
	return policy.New(f, f.namespace, f.tweakListOptions)
}

func (f *sharedInformerFactory) Rbac() rbac.Interface {
	return rbac.New(f, f.namespace, f.tweakListOptions)
}

func (f *sharedInformerFactory) Scheduling() scheduling.Interface {
	return scheduling.New(f, f.namespace, f.tweakListOptions)
}

func (f *sharedInformerFactory) Storage() storage.Interface {
	return storage.New(f, f.namespace, f.tweakListOptions)
}
