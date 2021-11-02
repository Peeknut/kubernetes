/*
Copyright 2017 The Kubernetes Authors.

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

package handlers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metainternalversionscheme "k8s.io/apimachinery/pkg/apis/meta/internalversion/scheme"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/audit"
	"k8s.io/apiserver/pkg/endpoints/handlers/fieldmanager"
	"k8s.io/apiserver/pkg/endpoints/handlers/finisher"
	"k8s.io/apiserver/pkg/endpoints/handlers/negotiation"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/features"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/apiserver/pkg/util/dryrun"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	utiltrace "k8s.io/utils/trace"
)

var namespaceGVK = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Namespace"}

//createHandler 是将数据写入到后端存储的方法，对于资源的操作都有相关的权限控制，
//在 createHandler 中首先会执行 decoder 和 admission 操作，
//然后调用 create 方法完成 resource 的创建，在 create 方法中会进行 validate 以及最终将数据保存到后端存储中。a
//dmit 操作即执行 kube-apiserver 中的 admission-plugins，admission-plugins 在 CreateKubeAPIServerConfig 中被初始化为了 admissionChain，
//其初始化的调用链为 CreateKubeAPIServerConfig --> buildGenericConfig --> s.Admission.ApplyTo --> a.GenericAdmission.ApplyTo --> a.Plugins.NewFromPlugins，
//最终在 a.Plugins.NewFromPlugins 中将所有已启用的 plugins 封装为 admissionChain，此处要执行的 admit 操作即执行 admission-plugins 中的 admit 操作。
func createHandler(r rest.NamedCreater, scope *RequestScope, admit admission.Interface, includeName bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		// For performance tracking purposes.
		trace := utiltrace.New("Create", traceFields(req)...)
		defer trace.LogIfLong(500 * time.Millisecond)

		if isDryRun(req.URL) && !utilfeature.DefaultFeatureGate.Enabled(features.DryRun) {
			scope.err(errors.NewBadRequest("the dryRun feature is disabled"), w, req)
			return
		}

		namespace, name, err := scope.Namer.Name(req)
		if err != nil {
			if includeName {
				// name was required, return
				scope.err(err, w, req)
				return
			}

			// otherwise attempt to look up the namespace
			namespace, err = scope.Namer.Namespace(req)
			if err != nil {
				scope.err(err, w, req)
				return
			}
		}

		// enforce a timeout of at most requestTimeoutUpperBound (34s) or less if the user-provided
		// timeout inside the parent context is lower than requestTimeoutUpperBound.
		ctx, cancel := context.WithTimeout(req.Context(), requestTimeoutUpperBound)
		defer cancel()
		outputMediaType, _, err := negotiation.NegotiateOutputMediaType(req, scope.Serializer, scope)
		if err != nil {
			scope.err(err, w, req)
			return
		}

		// 1、得到合适的SerializerInfo
		gv := scope.Kind.GroupVersion()
		s, err := negotiation.NegotiateInputSerializer(req, false, scope.Serializer)
		if err != nil {
			scope.err(err, w, req)
			return
		}

		// 2、找到合适的 decoder
		decoder := scope.Serializer.DecoderToVersion(s.Serializer, scope.HubGroupVersion)

		body, err := limitedReadBody(req, scope.MaxRequestBodyBytes)
		if err != nil {
			scope.err(err, w, req)
			return
		}

		options := &metav1.CreateOptions{}
		values := req.URL.Query()
		if err := metainternalversionscheme.ParameterCodec.DecodeParameters(values, scope.MetaGroupVersion, options); err != nil {
			err = errors.NewBadRequest(err.Error())
			scope.err(err, w, req)
			return
		}
		if errs := validation.ValidateCreateOptions(options); len(errs) > 0 {
			err := errors.NewInvalid(schema.GroupKind{Group: metav1.GroupName, Kind: "CreateOptions"}, "", errs)
			scope.err(err, w, req)
			return
		}
		options.TypeMeta.SetGroupVersionKind(metav1.SchemeGroupVersion.WithKind("CreateOptions"))

		defaultGVK := scope.Kind
		original := r.New()
		trace.Step("About to convert to expected version")
		// 3、decoder 解码
		obj, gvk, err := decoder.Decode(body, &defaultGVK, original)
		if err != nil {
			err = transformDecodeError(scope.Typer, err, original, gvk, body)
			scope.err(err, w, req)
			return
		}

		objGV := gvk.GroupVersion()
		if !scope.AcceptsGroupVersion(objGV) {
			err = errors.NewBadRequest(fmt.Sprintf("the API version in the data (%s) does not match the expected API version (%v)", objGV.String(), gv.String()))
			scope.err(err, w, req)
			return
		}
		trace.Step("Conversion done")

		// 从 obj 中获取 name、ns
		// On create, get name from new object if unset
		if len(name) == 0 {
			_, name, _ = scope.Namer.ObjectName(obj)
		}
		if len(namespace) == 0 && *gvk == namespaceGVK {
			namespace = name
		}
		ctx = request.WithNamespace(ctx, namespace)

		ae := request.AuditEventFrom(ctx)
		admit = admission.WithAudit(admit, ae)
		audit.LogRequestObject(ae, obj, objGV, scope.Resource, scope.Subresource, scope.Serializer)

		userInfo, _ := request.UserFrom(ctx)

		trace.Step("About to store object in database")
		// 4、执行 admit 操作，即执行 kube-apiserver 启动时加载的 admission-plugins，
		admissionAttributes := admission.NewAttributesRecord(obj, nil, scope.Kind, namespace, name, scope.Resource, scope.Subresource, admission.Create, options, dryrun.IsDryRun(options.DryRun), userInfo)
		requestFunc := func() (runtime.Object, error) {
			// 5、执行 create 操作
			return r.Create(
				ctx,
				name,
				obj,
				rest.AdmissionToValidateObjectFunc(admit, admissionAttributes, scope),
				options,
			)
		}
		// Dedup owner references before updating managed fields
		dedupOwnerReferencesAndAddWarning(obj, req.Context(), false)
		result, err := finisher.FinishRequest(ctx, func() (runtime.Object, error) {
			if scope.FieldManager != nil {
				liveObj, err := scope.Creater.New(scope.Kind)
				if err != nil {
					return nil, fmt.Errorf("failed to create new object (Create for %v): %v", scope.Kind, err)
				}
				obj = scope.FieldManager.UpdateNoErrors(liveObj, obj, managerOrUserAgent(options.FieldManager, req.UserAgent()))
				admit = fieldmanager.NewManagedFieldsValidatingAdmissionController(admit)
			}
			if mutatingAdmission, ok := admit.(admission.MutationInterface); ok && mutatingAdmission.Handles(admission.Create) {
				// 调用 admission chain 中插件的各个 Admit 方法
				if err := mutatingAdmission.Admit(ctx, admissionAttributes, scope); err != nil {
					return nil, err
				}
			}
			// Dedup owner references again after mutating admission happens
			dedupOwnerReferencesAndAddWarning(obj, req.Context(), true)
			result, err := requestFunc()
			// If the object wasn't committed to storage because it's serialized size was too large,
			// it is safe to remove managedFields (which can be large) and try again.
			if isTooLargeError(err) {
				if accessor, accessorErr := meta.Accessor(obj); accessorErr == nil {
					accessor.SetManagedFields(nil)
					result, err = requestFunc()
				}
			}
			return result, err
		})
		if err != nil {
			scope.err(err, w, req)
			return
		}
		trace.Step("Object stored in database")

		code := http.StatusCreated
		status, ok := result.(*metav1.Status)
		if ok && err == nil && status.Code == 0 {
			status.Code = int32(code)
		}

		transformResponseObject(ctx, scope, trace, req, w, code, outputMediaType, result)
	}
}

// 返回资源创建的 handler——参数具有name
// CreateNamedResource returns a function that will handle a resource creation with name.
func CreateNamedResource(r rest.NamedCreater, scope *RequestScope, admission admission.Interface) http.HandlerFunc {
	//createHandler 是将数据写入到后端存储的方法，对于资源的操作都有相关的权限控制，
	//在 createHandler 中首先会执行 decoder 和 admission 操作，
	//然后调用 create 方法完成 resource 的创建，在 create 方法中会进行 validate 以及最终将数据保存到后端存储中。a
	//dmit 操作即执行 kube-apiserver 中的 admission-plugins，admission-plugins 在 CreateKubeAPIServerConfig 中被初始化为了 admissionChain，
	//其初始化的调用链为 CreateKubeAPIServerConfig --> buildGenericConfig --> s.Admission.ApplyTo --> a.GenericAdmission.ApplyTo --> a.Plugins.NewFromPlugins，
	//最终在 a.Plugins.NewFromPlugins 中将所有已启用的 plugins 封装为 admissionChain，此处要执行的 admit 操作即执行 admission-plugins 中的 admit 操作。
	return createHandler(r, scope, admission, true)
}

// 返回资源创建的 handler——参数没有name
// CreateResource returns a function that will handle a resource creation.
func CreateResource(r rest.Creater, scope *RequestScope, admission admission.Interface) http.HandlerFunc {
	return createHandler(&namedCreaterAdapter{r}, scope, admission, false)
}

type namedCreaterAdapter struct {
	rest.Creater
}

func (c *namedCreaterAdapter) Create(ctx context.Context, name string, obj runtime.Object, createValidatingAdmission rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	return c.Creater.Create(ctx, obj, createValidatingAdmission, options)
}

// manager is assumed to be already a valid value, we need to make
// userAgent into a valid value too.
func managerOrUserAgent(manager, userAgent string) string {
	if manager != "" {
		return manager
	}
	return prefixFromUserAgent(userAgent)
}

// prefixFromUserAgent takes the characters preceding the first /, quote
// unprintable character and then trim what's beyond the
// FieldManagerMaxLength limit.
func prefixFromUserAgent(u string) string {
	m := strings.Split(u, "/")[0]
	buf := bytes.NewBuffer(nil)
	for _, r := range m {
		// Ignore non-printable characters
		if !unicode.IsPrint(r) {
			continue
		}
		// Only append if we have room for it
		if buf.Len()+utf8.RuneLen(r) > validation.FieldManagerMaxLength {
			break
		}
		buf.WriteRune(r)
	}
	return buf.String()
}
