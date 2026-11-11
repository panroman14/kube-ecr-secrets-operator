package v1alpha2

import (
	"github.com/panroman14/kube-ecr-secrets-operator/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AWSECRImagePullSecretSpec defines the desired state of AWSECRImagePullSecret.
// The pull secret is created in the object's own namespace.
// +kubebuilder:validation:XValidation:rule="!has(self.awsAccess.secretNamespace) || size(self.awsAccess.secretNamespace) == 0",message="awsAccess.secretNamespace is not used here: the credentials Secret is read from the object's own namespace"
type AWSECRImagePullSecretSpec struct {
	// Where the AWS credentials are.
	AWSAccess common.AWSAccess `json:"awsAccess"`

	// Name of the kubernetes.io/dockerconfigjson Secret the operator creates and keeps
	// refreshed. Reference it in imagePullSecrets. The operator never takes over an
	// existing Secret it did not create.
	//+kubebuilder:validation:MaxLength=253
	//+kubebuilder:validation:MinLength=1
	//+kubebuilder:validation:XValidation:rule="self == oldSelf",message="secretName is immutable"
	SecretName string `json:"secretName"`
}

// AWSECRImagePullSecretStatus defines the observed state of AwsecrimagePullSecret.
// It should always be reconstructable from the state of the cluster and/or outside world.
type AWSECRImagePullSecretStatus struct {
	//+listType=map
	//+listMapKey=type
	//+patchStrategy=merge
	//+patchMergeKey=type
	//+optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=aeps

// AWSECRImagePullSecret is the Schema for the awsecrimagepullsecrets API
// +k8s:openapi-gen=true
type AWSECRImagePullSecret struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AWSECRImagePullSecretSpec   `json:"spec,omitempty"`
	Status AWSECRImagePullSecretStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AWSECRImagePullSecretList contains a list of AwsecrimagePullSecret
type AWSECRImagePullSecretList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AWSECRImagePullSecret `json:"items"`
}
