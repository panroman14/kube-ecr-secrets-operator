/*
Copyright 2022.

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

package v1alpha2

import (
	"github.com/panroman14/kube-ecr-secrets-operator/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterAWSECRImagePullSecretSpec defines the desired state of ClusterAWSECRImagePullSecret.
// The same pull secret is created in every namespace listed in namespaces.
// +kubebuilder:validation:XValidation:rule="has(self.awsAccess.secretNamespace) && size(self.awsAccess.secretNamespace) > 0",message="awsAccess.secretNamespace is required: the namespace of the AWS credentials Secret"
type ClusterAWSECRImagePullSecretSpec struct {
	// Where the AWS credentials are.
	AWSAccess common.AWSAccess `json:"awsAccess"`

	// Name of the kubernetes.io/dockerconfigjson Secret the operator creates and keeps
	// refreshed in each namespace. Reference it in imagePullSecrets. The operator never
	// takes over an existing Secret it did not create.
	//+kubebuilder:validation:MaxLength=253
	//+kubebuilder:validation:MinLength=1
	//+kubebuilder:validation:XValidation:rule="self == oldSelf",message="secretName is immutable"
	SecretName string `json:"secretName"`

	// Namespaces to create the pull secret in.
	//+kubebuilder:validation:MinItems=1
	//+listType=set
	Namespaces []string `json:"namespaces"`
}

// ClusterAWSECRImagePullSecretStatus defines the observed state of ClusterAWSECRCredentials.
// It should always be reconstructable from the state of the cluster and/or outside world.
type ClusterAWSECRImagePullSecretStatus struct {
	//+listType=map
	//+listMapKey=type
	//+patchStrategy=merge
	//+patchMergeKey=type
	//+optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Namespaces that hold this object's pull secret. Used to delete the pull secret from
	// namespaces that were removed from spec.namespaces.
	//+listType=set
	//+optional
	Namespaces []string `json:"namespaces,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:scope=Cluster,shortName=caeps
//+kubebuilder:subresource:status

// ClusterAWSECRImagePullSecret is the Schema for the clusterawsecrcredentials API
type ClusterAWSECRImagePullSecret struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterAWSECRImagePullSecretSpec   `json:"spec,omitempty"`
	Status ClusterAWSECRImagePullSecretStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ClusterAWSECRImagePullSecretList contains a list of ClusterAWSECRCredentials
type ClusterAWSECRImagePullSecretList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterAWSECRImagePullSecret `json:"items"`
}
