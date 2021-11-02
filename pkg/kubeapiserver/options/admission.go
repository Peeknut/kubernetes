/*
Copyright 2018 The Kubernetes Authors.

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

package options

import (
	"fmt"
	"strings"

	"github.com/spf13/pflag"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/server"
	genericoptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/rest"
	"k8s.io/component-base/featuregate"
)

// AdmissionOptions holds the admission options.
// It is a wrap of generic AdmissionOptions.
type AdmissionOptions struct {
	// GenericAdmission holds the generic admission options.
	GenericAdmission *genericoptions.AdmissionOptions
	// DEPRECATED flag, should use EnabledAdmissionPlugins and DisabledAdmissionPlugins.
	// They are mutually exclusive, specify both will lead to an error.
	PluginNames []string
}

// NewAdmissionOptions creates a new instance of AdmissionOptions
// Note:
//  In addition it calls RegisterAllAdmissionPlugins to register
//  all kube-apiserver admission plugins.
//
//  Provides the list of RecommendedPluginOrder that holds sane values
//  that can be used by servers that don't care about admission chain.
//  Servers that do care can overwrite/append that field after creation.
func NewAdmissionOptions() *AdmissionOptions {
	// 1. 创建AdmissionOptions，并在里面注册了webhook的validating、mutating插件，还有 lifecycle 插件——具体的插件没有具体实现
	options := genericoptions.NewAdmissionOptions()
	//2. 注册所有的内置的admission plugins——指的是注册 pulgins 的生成方法
	// 注册内置所有的 admission 插件（如果自己新增了插件，需要在这里添加注册函数）
	// register all admission plugins
	RegisterAllAdmissionPlugins(options.Plugins)
	//3.设置 admission plugin顺序
	// set RecommendedPluginOrder
	options.RecommendedPluginOrder = AllOrderedPlugins
	//4.默认关闭的plugin列表
	// set DefaultOffPlugins
	options.DefaultOffPlugins = DefaultOffAdmissionPlugins()

	return &AdmissionOptions{
		GenericAdmission: options,
	}
}

// AddFlags adds flags related to admission for kube-apiserver to the specified FlagSet
func (a *AdmissionOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringSliceVar(&a.PluginNames, "admission-control", a.PluginNames, ""+
		"Admission is divided into two phases. "+
		"In the first phase, only mutating admission plugins run. "+
		"In the second phase, only validating admission plugins run. "+
		"The names in the below list may represent a validating plugin, a mutating plugin, or both. "+
		"The order of plugins in which they are passed to this flag does not matter. "+
		"Comma-delimited list of: "+strings.Join(a.GenericAdmission.Plugins.Registered(), ", ")+".")
	fs.MarkDeprecated("admission-control", "Use --enable-admission-plugins or --disable-admission-plugins instead. Will be removed in a future version.")
	fs.Lookup("admission-control").Hidden = false

	a.GenericAdmission.AddFlags(fs)
}

// Validate verifies flags passed to kube-apiserver AdmissionOptions.
// Kube-apiserver verifies PluginNames and then call generic AdmissionOptions.Validate.
func (a *AdmissionOptions) Validate() []error {
	if a == nil {
		return nil
	}
	var errs []error
	if a.PluginNames != nil &&
		(a.GenericAdmission.EnablePlugins != nil || a.GenericAdmission.DisablePlugins != nil) {
		errs = append(errs, fmt.Errorf("admission-control and enable-admission-plugins/disable-admission-plugins flags are mutually exclusive"))
	}

	registeredPlugins := sets.NewString(a.GenericAdmission.Plugins.Registered()...)
	for _, name := range a.PluginNames {
		if !registeredPlugins.Has(name) {
			errs = append(errs, fmt.Errorf("admission-control plugin %q is unknown", name))
		}
	}

	errs = append(errs, a.GenericAdmission.Validate()...)

	return errs
}

// 将 options 中 admission 的设置配置到 config 中
// ApplyTo adds the admission chain to the server configuration.
// Kube-apiserver just call generic AdmissionOptions.ApplyTo.
func (a *AdmissionOptions) ApplyTo(
	c *server.Config,
	informers informers.SharedInformerFactory,
	kubeAPIServerClientConfig *rest.Config,
	features featuregate.FeatureGate,
	pluginInitializers ...admission.PluginInitializer,
) error {
	if a == nil {
		return nil
	}

	// 默认情况下为nil
	if a.PluginNames != nil {
		// pass PluginNames to generic AdmissionOptions
		a.GenericAdmission.EnablePlugins, a.GenericAdmission.DisablePlugins = computePluginNames(a.PluginNames, a.GenericAdmission.RecommendedPluginOrder)
	}

	// 将 c.AdmissionControl 初始化：admission 插件的生成
	return a.GenericAdmission.ApplyTo(c, informers, kubeAPIServerClientConfig, features, pluginInitializers...)
}

// explicitly disable all plugins that are not in the enabled list
func computePluginNames(explicitlyEnabled []string, all []string) (enabled []string, disabled []string) {
	return explicitlyEnabled, sets.NewString(all...).Difference(sets.NewString(explicitlyEnabled...)).List()
}
