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

// Package app implements a server that runs a set of active
// components.  This includes replication controllers, service endpoints and
// nodes.
//
package app

import (
	"context"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	genericfeatures "k8s.io/apiserver/pkg/features"
	"k8s.io/apiserver/pkg/server/healthz"
	"k8s.io/apiserver/pkg/server/mux"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	cacheddiscovery "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/metadata/metadatainformer"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	certutil "k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/keyutil"
	cloudprovider "k8s.io/cloud-provider"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/cli/globalflag"
	"k8s.io/component-base/configz"
	"k8s.io/component-base/term"
	"k8s.io/component-base/version"
	"k8s.io/component-base/version/verflag"
	genericcontrollermanager "k8s.io/controller-manager/app"
	"k8s.io/controller-manager/pkg/clientbuilder"
	"k8s.io/controller-manager/pkg/informerfactory"
	"k8s.io/controller-manager/pkg/leadermigration"
	"k8s.io/klog/v2"

	"k8s.io/kubernetes/cmd/kube-controller-manager/app/config"
	"k8s.io/kubernetes/cmd/kube-controller-manager/app/options"
	kubectrlmgrconfig "k8s.io/kubernetes/pkg/controller/apis/config"
	serviceaccountcontroller "k8s.io/kubernetes/pkg/controller/serviceaccount"
	"k8s.io/kubernetes/pkg/serviceaccount"
)

const (
	// ControllerStartJitter is the Jitter used when starting controller managers
	ControllerStartJitter = 1.0
	// ConfigzName is the name used for register kube-controller manager /configz, same with GroupName.
	ConfigzName = "kubecontrollermanager.config.k8s.io"
)

// ControllerLoopMode is the kube-controller-manager's mode of running controller loops that are cloud provider dependent
type ControllerLoopMode int

const (
	// IncludeCloudLoops means the kube-controller-manager include the controller loops that are cloud provider dependent
	IncludeCloudLoops ControllerLoopMode = iota
	// ExternalLoops means the kube-controller-manager exclude the controller loops that are cloud provider dependent
	ExternalLoops
)

// TODO: delete this check after insecure flags removed in v1.24
func checkNonZeroInsecurePort(fs *pflag.FlagSet) error {
	val, err := fs.GetInt("port")
	if err != nil {
		return err
	}
	if val != 0 {
		return fmt.Errorf("invalid port value %d: only zero is allowed", val)
	}
	return nil
}

// -1-
// NewControllerManagerCommand creates a *cobra.Command object with default parameters
func NewControllerManagerCommand() *cobra.Command {
	// 1. 创建 options
	s, err := options.NewKubeControllerManagerOptions()
	if err != nil {
		klog.Fatalf("unable to initialize command options: %v", err)
	}

	// 2. 创建 command
	cmd := &cobra.Command{
		Use: "kube-controller-manager",
		Long: `The Kubernetes controller manager is a daemon that embeds
the core control loops shipped with Kubernetes. In applications of robotics and
automation, a control loop is a non-terminating loop that regulates the state of
the system. In Kubernetes, a controller is a control loop that watches the shared
state of the cluster through the apiserver and makes changes attempting to move the
current state towards the desired state. Examples of controllers that ship with
Kubernetes today are the replication controller, endpoints controller, namespace
controller, and serviceaccounts controller.`,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			// silence client-go warnings.
			// kube-controller-manager generically watches APIs (including deprecated ones),
			// and CI ensures it works properly against matching kube-apiserver versions.
			restclient.SetDefaultWarningHandler(restclient.NoWarnings{})
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			verflag.PrintAndExitIfRequested()
			cliflag.PrintFlags(cmd.Flags())  // log 记录传入的参数

			err := checkNonZeroInsecurePort(cmd.Flags())
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}

			// 4. 根据 option 创建 config
			// 只是简单的将输入参数--controllers 的内容赋值给 c 的相关字段，并不进行真正的处理
			c, err := s.Config(KnownControllers(), ControllersDisabledByDefault.List())  // 这里进去可以看到怎么处理参数 --controllers 的
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}

			// 5. 补全 config
			// 6. 运行 Run 函数（一般传入 config、stop channel）
			if err := Run(c.Complete(), wait.NeverStop); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
		},
		Args: func(cmd *cobra.Command, args []string) error {
			for _, arg := range args {
				if len(arg) > 0 {
					return fmt.Errorf("%q does not take any arguments, got %q", cmd.CommandPath(), args)
				}
			}
			return nil
		},
	}

	// 3. 添加参数
	fs := cmd.Flags()
	namedFlagSets := s.Flags(KnownControllers(), ControllersDisabledByDefault.List())  // 添加了 kcm 的参数
	verflag.AddFlags(namedFlagSets.FlagSet("global"))
	globalflag.AddGlobalFlags(namedFlagSets.FlagSet("global"), cmd.Name())
	registerLegacyGlobalFlags(namedFlagSets)
	for _, f := range namedFlagSets.FlagSets {
		fs.AddFlagSet(f)
	}

	cols, _, _ := term.TerminalSize(cmd.OutOrStdout())
	cliflag.SetUsageAndHelpFunc(cmd, namedFlagSets, cols)

	return cmd
}

// ResyncPeriod returns a function which generates a duration each time it is
// invoked; this is so that multiple controllers don't get into lock-step and all
// hammer the apiserver with list requests simultaneously.
func ResyncPeriod(c *config.CompletedConfig) func() time.Duration {
	return func() time.Duration {
		factor := rand.Float64() + 1
		return time.Duration(float64(c.ComponentConfig.Generic.MinResyncPeriod.Nanoseconds()) * factor)
	}
}

// Run 一般接收参数：1）config；2）stop channel
// 	   函数一般不会退出
// Run runs the KubeControllerManagerOptions.  This should never exit.
func Run(c *config.CompletedConfig, stopCh <-chan struct{}) error {
	// To help debugging, immediately log version
	klog.Infof("Version: %+v", version.Get())

	// 配置存放到全局配置中，configz实现了一个全局的配置功能，并且可以安装到HTTP服务中对外提供http访问配置的能力
	if cfgz, err := configz.New(ConfigzName); err == nil {  // 貌似是注册 config 的，即一个 configname，应该只能注册一次
		cfgz.Set(c.ComponentConfig)
	} else {
		klog.Errorf("unable to register configz: %v", err)
	}

	// Q：这里的健康检查是检查什么？
	// Setup any healthz checks we will want to use.
	var checks []healthz.HealthChecker
	var electionChecker *leaderelection.HealthzAdaptor
	if c.ComponentConfig.Generic.LeaderElection.LeaderElect {
		electionChecker = leaderelection.NewLeaderHealthzAdaptor(time.Second * 20)
		checks = append(checks, electionChecker)
	}

	// Q：kcm 的 http server 是什么？
	// Start the controller manager HTTP server
	// unsecuredMux is the handler for these controller *after* authn/authz filters have been applied
	var unsecuredMux *mux.PathRecorderMux
	if c.SecureServing != nil {
		unsecuredMux = genericcontrollermanager.NewBaseHandler(&c.ComponentConfig.Generic.Debugging, checks...)
		handler := genericcontrollermanager.BuildHandlerChain(unsecuredMux, &c.Authorization, &c.Authentication)
		// TODO: handle stoppedCh returned by c.SecureServing.Serve
		if _, err := c.SecureServing.Serve(handler, 0, stopCh); err != nil {
			return err
		}
	}

	// 貌似是构建器，用于创建到 API Server 的各种连接的构建器。
	// 由于 controller 中会有多种到 API Server 的连接，为了区分它们，就需要基于构建器来完成，
	// 在构建时会生成不同的 Agent Name，从而能够根据 Agent Name 进行区分
	clientBuilder, rootClientBuilder := createClientBuilders(c)

	saTokenControllerInitFunc := serviceAccountTokenControllerStarter{rootClientBuilder: rootClientBuilder}.startServiceAccountTokenController  // 函数指针

	// 成为 leader 之后才会运行的 run 方法
	// 参数：1）上下文；2）serviceaccount-token controlelr 的启动函数；3）其他 controller 的启动函数
	run := func(ctx context.Context, startSATokenController InitFunc, initializersFunc ControllerInitializersFunc) {

		// 1. 创建上下文
		controllerContext, err := CreateControllerContext(c, rootClientBuilder, clientBuilder, ctx.Done())  // ctx.Done() 返回一个 channel，controllerContext 中包含了 c
		if err != nil {
			klog.Fatalf("error building controller context: %v", err)
		}
		// 2. 启动控制器
		controllerInitializers := initializersFunc(controllerContext.LoopMode)  // 返回所有controller的启动函数
		if err := StartControllers(controllerContext, startSATokenController, controllerInitializers, unsecuredMux); err != nil {
			klog.Fatalf("error starting controllers: %v", err)
		}

		// 3. 启动 informer
		controllerContext.InformerFactory.Start(controllerContext.Stop)  // // 启动Informer工厂实例，在这里会启动各种类型的通知实例，新的资源变更，会源源不断的作为事件传输过来。
		controllerContext.ObjectOrMetadataInformerFactory.Start(controllerContext.Stop)
		close(controllerContext.InformersStarted)  // 这时候可以安全启动Informers，然而，Informers其实已经在InformerFactory中启动了。？？？？

		select {}  // 阻塞
	}

	// No leader election, run directly
	if !c.ComponentConfig.Generic.LeaderElection.LeaderElect {
		run(context.TODO(), saTokenControllerInitFunc, NewControllerInitializers)  // context.TODO() 返回一个空的 context
		panic("unreachable")
	}

	id, err := os.Hostname()
	if err != nil {
		return err
	}

	// add a uniquifier so that two processes on the same host don't accidentally both become active
	id = id + "_" + string(uuid.NewUUID())

	// leaderMigrator will be non-nil if and only if Leader Migration is enabled.
	var leaderMigrator *leadermigration.LeaderMigrator = nil

	// startSATokenController will be original saTokenControllerInitFunc if leader migration is not enabled.
	startSATokenController := saTokenControllerInitFunc

	// If leader migration is enabled, create the LeaderMigrator and prepare for migration
	if leadermigration.Enabled(&c.ComponentConfig.Generic) {
		klog.Infof("starting leader migration")

		leaderMigrator = leadermigration.NewLeaderMigrator(&c.ComponentConfig.Generic.LeaderMigration,
			"kube-controller-manager")

		// Wrap saTokenControllerInitFunc to signal readiness for migration after starting
		//  the controller.
		startSATokenController = func(ctx ControllerContext) (http.Handler, bool, error) {
			defer close(leaderMigrator.MigrationReady)
			return saTokenControllerInitFunc(ctx)
		}
	}

	// Start the main lock
	go leaderElectAndRun(c, id, electionChecker,
		c.ComponentConfig.Generic.LeaderElection.ResourceLock,
		c.ComponentConfig.Generic.LeaderElection.ResourceName,
		leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				initializersFunc := NewControllerInitializers
				if leaderMigrator != nil {
					// If leader migration is enabled, we should start only non-migrated controllers
					//  for the main lock.
					initializersFunc = createInitializersFunc(leaderMigrator.FilterFunc, leadermigration.ControllerNonMigrated)
					klog.Info("leader migration: starting main controllers.")
				}
				run(ctx, startSATokenController, initializersFunc)
			},
			OnStoppedLeading: func() {
				klog.Fatalf("leaderelection lost")
			},
		})

	// If Leader Migration is enabled, proceed to attempt the migration lock.
	if leaderMigrator != nil {
		// Wait for Service Account Token Controller to start before acquiring the migration lock.
		// At this point, the main lock must have already been acquired, or the KCM process already exited.
		// We wait for the main lock before acquiring the migration lock to prevent the situation
		//  where KCM instance A holds the main lock while KCM instance B holds the migration lock.
		<-leaderMigrator.MigrationReady

		// Start the migration lock.
		go leaderElectAndRun(c, id, electionChecker,
			c.ComponentConfig.Generic.LeaderMigration.ResourceLock,
			c.ComponentConfig.Generic.LeaderMigration.LeaderName,
			leaderelection.LeaderCallbacks{
				OnStartedLeading: func(ctx context.Context) {
					klog.Info("leader migration: starting migrated controllers.")
					// DO NOT start saTokenController under migration lock
					run(ctx, nil, createInitializersFunc(leaderMigrator.FilterFunc, leadermigration.ControllerMigrated))
				},
				OnStoppedLeading: func() {
					klog.Fatalf("migration leaderelection lost")
				},
			})
	}

	select {}  // select 类似 switch，如果没有一个 case 可以运行，那么会一直阻塞。这里就会一直阻塞，不会结束这个函数
}

// ControllerContext defines the context object for controller
type ControllerContext struct {
	// ClientBuilder will provide a client for this controller to use
	ClientBuilder clientbuilder.ControllerClientBuilder

	// InformerFactory gives access to informers for the controller.
	InformerFactory informers.SharedInformerFactory

	// ObjectOrMetadataInformerFactory gives access to informers for typed resources
	// and dynamic resources by their metadata. All generic controllers currently use
	// object metadata - if a future controller needs access to the full object this
	// would become GenericInformerFactory and take a dynamic client.
	ObjectOrMetadataInformerFactory informerfactory.InformerFactory

	// ComponentConfig provides access to init options for a given controller
	ComponentConfig kubectrlmgrconfig.KubeControllerManagerConfiguration

	// DeferredDiscoveryRESTMapper is a RESTMapper that will defer
	// initialization of the RESTMapper until the first mapping is
	// requested.
	RESTMapper *restmapper.DeferredDiscoveryRESTMapper

	// AvailableResources is a map listing currently available resources
	AvailableResources map[schema.GroupVersionResource]bool

	// Cloud is the cloud provider interface for the controllers to use.
	// It must be initialized and ready to use.
	Cloud cloudprovider.Interface

	// Control for which control loops to be run
	// IncludeCloudLoops is for a kube-controller-manager running all loops
	// ExternalLoops is for a kube-controller-manager running with a cloud-controller-manager
	LoopMode ControllerLoopMode

	// Stop is the stop channel
	Stop <-chan struct{}

	// InformersStarted is closed after all of the controllers have been initialized and are running.  After this point it is safe,
	// for an individual controller to start the shared informers. Before it is closed, they should not.
	InformersStarted chan struct{}

	// ResyncPeriod generates a duration each time it is invoked; this is so that
	// multiple controllers don't get into lock-step and all hammer the apiserver
	// with list requests simultaneously.
	ResyncPeriod func() time.Duration
}

// IsControllerEnabled checks if the context's controllers enabled or not
// 判断controller 是否开启
func (c ControllerContext) IsControllerEnabled(name string) bool {
	return genericcontrollermanager.IsControllerEnabled(name, ControllersDisabledByDefault, c.ComponentConfig.Generic.Controllers)
}

// InitFunc is used to launch a particular controller.  It may run additional "should I activate checks".
// Any error returned will cause the controller process to `Fatal`
// The bool indicates whether the controller was enabled.
type InitFunc func(ctx ControllerContext) (debuggingHandler http.Handler, enabled bool, err error)

// ControllerInitializersFunc is used to create a collection of initializers
//  given the loopMode.
type ControllerInitializersFunc func(loopMode ControllerLoopMode) (initializers map[string]InitFunc)

var _ ControllerInitializersFunc = NewControllerInitializers

// KnownControllers returns all known controllers's name
// 返回所有的已知的controller名字，包括默认开启和关闭的
func KnownControllers() []string {
	ret := sets.StringKeySet(NewControllerInitializers(IncludeCloudLoops))

	// add "special" controllers that aren't initialized normally.  These controllers cannot be initialized
	// using a normal function.  The only known special case is the SA token controller which *must* be started
	// first to ensure that the SA tokens for future controllers will exist.  Think very carefully before adding
	// to this list.
	ret.Insert(
		saTokenControllerName,
	)

	return ret.List()
}

// ControllersDisabledByDefault is the set of controllers which is disabled by default
var ControllersDisabledByDefault = sets.NewString(
	"bootstrapsigner",
	"tokencleaner",
)

const (
	saTokenControllerName = "serviceaccount-token"
)

// NewControllerInitializers is a public map of named controller groups (you can start more than one in an init func)
// paired to their InitFunc.  This allows for structured downstream composition and subdivision.
func NewControllerInitializers(loopMode ControllerLoopMode) map[string]InitFunc {
	controllers := map[string]InitFunc{}
	controllers["endpoint"] = startEndpointController
	controllers["endpointslice"] = startEndpointSliceController
	controllers["endpointslicemirroring"] = startEndpointSliceMirroringController
	controllers["replicationcontroller"] = startReplicationController
	controllers["podgc"] = startPodGCController
	controllers["resourcequota"] = startResourceQuotaController
	controllers["namespace"] = startNamespaceController
	controllers["serviceaccount"] = startServiceAccountController
	controllers["garbagecollector"] = startGarbageCollectorController
	controllers["daemonset"] = startDaemonSetController
	controllers["job"] = startJobController
	controllers["deployment"] = startDeploymentController
	controllers["replicaset"] = startReplicaSetController
	controllers["horizontalpodautoscaling"] = startHPAController
	controllers["disruption"] = startDisruptionController
	controllers["statefulset"] = startStatefulSetController
	controllers["cronjob"] = startCronJobController
	controllers["csrsigning"] = startCSRSigningController
	controllers["csrapproving"] = startCSRApprovingController
	controllers["csrcleaner"] = startCSRCleanerController
	controllers["ttl"] = startTTLController
	controllers["bootstrapsigner"] = startBootstrapSignerController
	controllers["tokencleaner"] = startTokenCleanerController
	controllers["nodeipam"] = startNodeIpamController
	controllers["nodelifecycle"] = startNodeLifecycleController
	if loopMode == IncludeCloudLoops {
		controllers["service"] = startServiceController
		controllers["route"] = startRouteController
		controllers["cloud-node-lifecycle"] = startCloudNodeLifecycleController
		// TODO: volume controller into the IncludeCloudLoops only set.
	}
	controllers["persistentvolume-binder"] = startPersistentVolumeBinderController
	controllers["attachdetach"] = startAttachDetachController
	controllers["persistentvolume-expander"] = startVolumeExpandController
	controllers["clusterrole-aggregation"] = startClusterRoleAggregrationController
	controllers["pvc-protection"] = startPVCProtectionController
	controllers["pv-protection"] = startPVProtectionController
	controllers["ttl-after-finished"] = startTTLAfterFinishedController
	controllers["root-ca-cert-publisher"] = startRootCACertPublisher
	controllers["ephemeral-volume"] = startEphemeralVolumeController
	if utilfeature.DefaultFeatureGate.Enabled(genericfeatures.APIServerIdentity) &&
		utilfeature.DefaultFeatureGate.Enabled(genericfeatures.StorageVersionAPI) {
		controllers["storage-version-gc"] = startStorageVersionGCController
	}

	return controllers
}

// GetAvailableResources gets the map which contains all available resources of the apiserver
// TODO: In general, any controller checking this needs to be dynamic so
// users don't have to restart their controller manager if they change the apiserver.
// Until we get there, the structure here needs to be exposed for the construction of a proper ControllerContext.
func GetAvailableResources(clientBuilder clientbuilder.ControllerClientBuilder) (map[schema.GroupVersionResource]bool, error) {
	client := clientBuilder.ClientOrDie("controller-discovery")
	discoveryClient := client.Discovery()
	_, resourceMap, err := discoveryClient.ServerGroupsAndResources()
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("unable to get all supported resources from server: %v", err))
	}
	if len(resourceMap) == 0 {
		return nil, fmt.Errorf("unable to get any supported resources from server")
	}

	allResources := map[schema.GroupVersionResource]bool{}
	for _, apiResourceList := range resourceMap {
		version, err := schema.ParseGroupVersion(apiResourceList.GroupVersion)
		if err != nil {
			return nil, err
		}
		for _, apiResource := range apiResourceList.APIResources {
			allResources[version.WithResource(apiResource.Name)] = true
		}
	}

	return allResources, nil
}

// CreateControllerContext creates a context struct containing references to resources needed by the
// controllers such as the cloud provider and clientBuilder. rootClientBuilder is only used for
// the shared-informers client and token controller.
func CreateControllerContext(s *config.CompletedConfig, rootClientBuilder, clientBuilder clientbuilder.ControllerClientBuilder, stop <-chan struct{}) (ControllerContext, error) {
	versionedClient := rootClientBuilder.ClientOrDie("shared-informers")
	sharedInformers := informers.NewSharedInformerFactory(versionedClient, ResyncPeriod(s)())

	metadataClient := metadata.NewForConfigOrDie(rootClientBuilder.ConfigOrDie("metadata-informers"))
	metadataInformers := metadatainformer.NewSharedInformerFactory(metadataClient, ResyncPeriod(s)())

	// If apiserver is not running we should wait for some time and fail only then. This is particularly
	// important when we start apiserver and controller manager at the same time.
	if err := genericcontrollermanager.WaitForAPIServer(versionedClient, 10*time.Second); err != nil {
		return ControllerContext{}, fmt.Errorf("failed to wait for apiserver being healthy: %v", err)
	}

	// Use a discovery client capable of being refreshed.
	discoveryClient := rootClientBuilder.DiscoveryClientOrDie("controller-discovery")
	cachedClient := cacheddiscovery.NewMemCacheClient(discoveryClient)
	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(cachedClient)
	go wait.Until(func() {
		restMapper.Reset()
	}, 30*time.Second, stop)

	availableResources, err := GetAvailableResources(rootClientBuilder)
	if err != nil {
		return ControllerContext{}, err
	}

	cloud, loopMode, err := createCloudProvider(s.ComponentConfig.KubeCloudShared.CloudProvider.Name, s.ComponentConfig.KubeCloudShared.ExternalCloudVolumePlugin,
		s.ComponentConfig.KubeCloudShared.CloudProvider.CloudConfigFile, s.ComponentConfig.KubeCloudShared.AllowUntaggedCloud, sharedInformers)
	if err != nil {
		return ControllerContext{}, err
	}

	ctx := ControllerContext{
		ClientBuilder:                   clientBuilder,
		InformerFactory:                 sharedInformers,
		ObjectOrMetadataInformerFactory: informerfactory.NewInformerFactory(sharedInformers, metadataInformers),
		ComponentConfig:                 s.ComponentConfig,
		RESTMapper:                      restMapper,
		AvailableResources:              availableResources,
		Cloud:                           cloud,
		LoopMode:                        loopMode,
		Stop:                            stop,
		InformersStarted:                make(chan struct{}),
		ResyncPeriod:                    ResyncPeriod(s),
	}
	return ctx, nil
}

// 遍历controllers（包含了所有的controller的名字，包括默认开启和关闭的），根据ControllerContext判断其是否开启， 启动开启的 controllers
// StartControllers starts a set of controllers with a specified ControllerContext
func StartControllers(ctx ControllerContext, startSATokenController InitFunc, controllers map[string]InitFunc, unsecuredMux *mux.PathRecorderMux) error {
	// Always start the SA token controller first using a full-power client, since it needs to mint tokens for the rest
	// If this fails, just return here and fail since other controllers won't be able to get credentials.
	if startSATokenController != nil {
		if _, _, err := startSATokenController(ctx); err != nil {
			return err
		}
	}

	// Initialize the cloud provider with a reference to the clientBuilder only after token controller
	// has started in case the cloud provider uses the client builder.
	if ctx.Cloud != nil {
		ctx.Cloud.Initialize(ctx.ClientBuilder, ctx.Stop)
	}

	for controllerName, initFn := range controllers {
		if !ctx.IsControllerEnabled(controllerName) {  // 判断参数 --controllers，是否含有这个 controller 的相关信息
			klog.Warningf("%q is disabled", controllerName)
			continue
		}

		time.Sleep(wait.Jitter(ctx.ComponentConfig.Generic.ControllerStartInterval.Duration, ControllerStartJitter))  // 等待启动控制器之间的时间间隔

		klog.V(1).Infof("Starting %q", controllerName)
		debugHandler, started, err := initFn(ctx)
		if err != nil {
			klog.Errorf("Error starting %q", controllerName)
			return err
		}
		if !started {
			klog.Warningf("Skipping %q", controllerName)
			continue
		}
		if debugHandler != nil && unsecuredMux != nil {
			basePath := "/debug/controllers/" + controllerName
			unsecuredMux.UnlistedHandle(basePath, http.StripPrefix(basePath, debugHandler))
			unsecuredMux.UnlistedHandlePrefix(basePath+"/", http.StripPrefix(basePath, debugHandler))
		}
		klog.Infof("Started %q", controllerName)
	}

	return nil
}

// service account token controller有点特殊，它必须要优先启动，为其他controller设置许可权，
// 并且它不能使用普通的client builder，只能使用root client builder。
// 它也不能包含在普通的初始化，必须优先启动。
// serviceAccountTokenControllerStarter is special because it must run first to set up permissions for other controllers.
// It cannot use the "normal" client builder, so it tracks its own. It must also avoid being included in the "normal"
// init map so that it can always run first.
type serviceAccountTokenControllerStarter struct {
	rootClientBuilder clientbuilder.ControllerClientBuilder
}

//ServiceAccountToken控制器的主要功能：
//捕捉两种资源：ServiceAccount和Secrets的变化事件，基于事件中的对象，进行ServiceAccount与Token的关联检查，进行不要的ServiceAccount与Secrets的关联处理和缓存中非法数据的删除操作。
func (c serviceAccountTokenControllerStarter) startServiceAccountTokenController(ctx ControllerContext) (http.Handler, bool, error) {
	if !ctx.IsControllerEnabled(saTokenControllerName) {
		klog.Warningf("%q is disabled", saTokenControllerName)
		return nil, false, nil
	}

	// 1. service-account controller 的私钥，用于签发 token
	if len(ctx.ComponentConfig.SAController.ServiceAccountKeyFile) == 0 {
		klog.Warningf("%q is disabled because there is no private key", saTokenControllerName)
		return nil, false, nil
	}
	privateKey, err := keyutil.PrivateKeyFromFile(ctx.ComponentConfig.SAController.ServiceAccountKeyFile)
	if err != nil {
		return nil, true, fmt.Errorf("error reading key for service account token controller: %v", err)
	}

	// 2. CA 证书
	var rootCA []byte
	if ctx.ComponentConfig.SAController.RootCAFile != "" {
		if rootCA, err = readCA(ctx.ComponentConfig.SAController.RootCAFile); err != nil {
			return nil, true, fmt.Errorf("error parsing root-ca-file at %s: %v", ctx.ComponentConfig.SAController.RootCAFile, err)
		}
	} else {
		rootCA = c.rootClientBuilder.ConfigOrDie("tokens-controller").CAData
	}

	// 3. 创建并执行 tokencontroller 实例
	tokenGenerator, err := serviceaccount.JWTTokenGenerator(serviceaccount.LegacyIssuer, privateKey)
	if err != nil {
		return nil, false, fmt.Errorf("failed to build token generator: %v", err)
	}
	controller, err := serviceaccountcontroller.NewTokensController(
		ctx.InformerFactory.Core().V1().ServiceAccounts(),  // 注意InformerFactory仍然采用通用的client builder
		ctx.InformerFactory.Core().V1().Secrets(),
		c.rootClientBuilder.ClientOrDie("tokens-controller"),
		serviceaccountcontroller.TokensControllerOptions{
			TokenGenerator: tokenGenerator,
			RootCA:         rootCA,
		},
	)
	if err != nil {
		return nil, true, fmt.Errorf("error creating Tokens controller: %v", err)
	}
	go controller.Run(int(ctx.ComponentConfig.SAController.ConcurrentSATokenSyncs), ctx.Stop)

	// 4. 优先启动ServiceAccounts和Secrets
	// start the first set of informers now so that other controllers can start
	ctx.InformerFactory.Start(ctx.Stop)

	return nil, true, nil
}

func readCA(file string) ([]byte, error) {
	rootCA, err := ioutil.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if _, err := certutil.ParseCertsPEM(rootCA); err != nil {
		return nil, err
	}

	return rootCA, err
}

// createClientBuilders creates clientBuilder and rootClientBuilder from the given configuration
func createClientBuilders(c *config.CompletedConfig) (clientBuilder clientbuilder.ControllerClientBuilder, rootClientBuilder clientbuilder.ControllerClientBuilder) {
	rootClientBuilder = clientbuilder.SimpleControllerClientBuilder{
		ClientConfig: c.Kubeconfig,
	}
	if c.ComponentConfig.KubeCloudShared.UseServiceAccountCredentials {
		if len(c.ComponentConfig.SAController.ServiceAccountKeyFile) == 0 {
			// It's possible another controller process is creating the tokens for us.
			// If one isn't, we'll timeout and exit when our client builder is unable to create the tokens.
			klog.Warningf("--use-service-account-credentials was specified without providing a --service-account-private-key-file")
		}

		clientBuilder = clientbuilder.NewDynamicClientBuilder(
			restclient.AnonymousClientConfig(c.Kubeconfig),
			c.Client.CoreV1(),
			metav1.NamespaceSystem)
	} else {
		clientBuilder = rootClientBuilder
	}
	return
}

// leaderElectAndRun runs the leader election, and runs the callbacks once the leader lease is acquired.
// TODO: extract this function into staging/controller-manager
func leaderElectAndRun(c *config.CompletedConfig, lockIdentity string, electionChecker *leaderelection.HealthzAdaptor, resourceLock string, leaseName string, callbacks leaderelection.LeaderCallbacks) {
	rl, err := resourcelock.NewFromKubeconfig(resourceLock,
		c.ComponentConfig.Generic.LeaderElection.ResourceNamespace,
		leaseName,
		resourcelock.ResourceLockConfig{
			Identity:      lockIdentity,
			EventRecorder: c.EventRecorder,
		},
		c.Kubeconfig,
		c.ComponentConfig.Generic.LeaderElection.RenewDeadline.Duration)
	if err != nil {
		klog.Fatalf("error creating lock: %v", err)
	}

	leaderelection.RunOrDie(context.TODO(), leaderelection.LeaderElectionConfig{
		Lock:          rl,
		LeaseDuration: c.ComponentConfig.Generic.LeaderElection.LeaseDuration.Duration,
		RenewDeadline: c.ComponentConfig.Generic.LeaderElection.RenewDeadline.Duration,
		RetryPeriod:   c.ComponentConfig.Generic.LeaderElection.RetryPeriod.Duration,
		Callbacks:     callbacks,
		WatchDog:      electionChecker,
		Name:          leaseName,
	})

	panic("unreachable")
}

// createInitializersFunc creates a initializersFunc that returns all initializer
//  with expected as the result after filtering through filterFunc.
func createInitializersFunc(filterFunc leadermigration.FilterFunc, expected leadermigration.FilterResult) ControllerInitializersFunc {
	return func(loopMode ControllerLoopMode) map[string]InitFunc {
		initializers := make(map[string]InitFunc)
		for name, initializer := range NewControllerInitializers(loopMode) {
			if filterFunc(name) == expected {
				initializers[name] = initializer
			}
		}
		return initializers
	}
}
