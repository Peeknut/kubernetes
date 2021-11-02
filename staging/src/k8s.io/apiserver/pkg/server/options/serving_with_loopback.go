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

	"github.com/google/uuid"

	"k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/dynamiccertificates"
	"k8s.io/client-go/rest"
	certutil "k8s.io/client-go/util/cert"
)

type SecureServingOptionsWithLoopback struct {
	*SecureServingOptions
}

func (o *SecureServingOptions) WithLoopback() *SecureServingOptionsWithLoopback {
	return &SecureServingOptionsWithLoopback{o}
}

// 生成 loopbackClientConfig
// ApplyTo fills up serving information in the server configuration.
func (s *SecureServingOptionsWithLoopback) ApplyTo(secureServingInfo **server.SecureServingInfo, loopbackClientConfig **rest.Config) error {
	if s == nil || s.SecureServingOptions == nil || secureServingInfo == nil {
		return nil
	}

	// 这里根据 s 的配置生成了 secureServingInfo（config）中的 cert（包括了 server 的 tls 配置） 和 SNICerts 字段
	if err := s.SecureServingOptions.ApplyTo(secureServingInfo); err != nil {
		return err
	}

	if *secureServingInfo == nil || loopbackClientConfig == nil {
		return nil
	}

	// 因为生成自签证书的时候，会生成 ca.key、ca.crt、server.key、server.crt。
	// 其中 crt 会使用 key 中的公钥进行生成。返回的 certPem 中会包含 ca.crt、server.crt。这里的 ca.crt 和目录 /etc/kubernetes/pki 中的 ca.crt 不同
	// 在 secureServingInfo（config）中的 SNICerts 字段中添加 自签证书的情况
	//本地回环地址访问apiserver的安全端口，需要生成自签名证书和秘钥（存储在apiserver中，这里是单向tls，即client请求server的安全端口）。本地回环地址访问的host域名是"apiserver-loopback-client"，
	//该域名及对应的自签名证书和秘钥最终存入secureServingInfo的SNICerts中。即本地回环地址访问apiserver的host（"apiserver-loopback-client"），
	//apiserver是通过SNICerts找到host对应的证书和秘钥返回给本地客户端。
	// https://github.com/yangyumo123/kubernetes-source-analysis/blob/master/kube-apiserver/run/createKubeAPIServerConfig.md
	// create self-signed cert+key with the fake server.LoopbackClientServerNameOverride and
	// let the server return it when the loopback client connects.
	certPem, keyPem, err := certutil.GenerateSelfSignedCertKey(server.LoopbackClientServerNameOverride, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to generate self-signed certificate for loopback connection: %v", err)
	}
	// certPem 中会包含 ca.crt（自签的）、server.crt
	// keyPem 只包含了 server.key
	certProvider, err := dynamiccertificates.NewStaticSNICertKeyContent("self-signed loopback", certPem, keyPem, server.LoopbackClientServerNameOverride)
	if err != nil {
		return fmt.Errorf("failed to generate self-signed certificate for loopback connection: %v", err)
	}

	// Write to the front of SNICerts so that this overrides any other certs with the same name
	(*secureServingInfo).SNICerts = append([]dynamiccertificates.SNICertKeyContentProvider{certProvider}, (*secureServingInfo).SNICerts...)

	secureLoopbackClientConfig, err := (*secureServingInfo).NewLoopbackClientConfig(uuid.New().String(), certPem)
	switch {
	// if we failed and there's no fallback loopback client config, we need to fail
	case err != nil && *loopbackClientConfig == nil:
		(*secureServingInfo).SNICerts = (*secureServingInfo).SNICerts[1:]
		return err

	// if we failed, but we already have a fallback loopback client config (usually insecure), allow it
	case err != nil && *loopbackClientConfig != nil:

	default:
		*loopbackClientConfig = secureLoopbackClientConfig
	}

	return nil
}
