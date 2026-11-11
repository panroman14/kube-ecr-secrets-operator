package common

// Keys the operator reads from the AWS credentials Secret.
const (
	AccessKeyIDKey     = "AWS_ACCESS_KEY_ID"
	SecretAccessKeyKey = "AWS_SECRET_ACCESS_KEY"
)

// AWSAccess points to the AWS credentials used to request ECR authorization tokens.
// The credentials themselves live in a Kubernetes Secret, never in this object.
type AWSAccess struct {
	// Name of a Secret holding the IAM user's keys under AWS_ACCESS_KEY_ID and
	// AWS_SECRET_ACCESS_KEY. The IAM user only needs ecr:GetAuthorizationToken for the
	// token, plus pull rights (ecr:BatchGetImage, ecr:GetDownloadUrlForLayer,
	// ecr:BatchCheckLayerAvailability) on the repositories the nodes pull from.
	//+kubebuilder:validation:MinLength=1
	//+kubebuilder:validation:MaxLength=253
	SecretName string `json:"secretName"`

	// Namespace of that Secret. Required for ClusterAWSECRImagePullSecret.
	// For AWSECRImagePullSecret it must be empty or equal to the object's namespace.
	//+optional
	//+kubebuilder:validation:MaxLength=63
	SecretNamespace string `json:"secretNamespace,omitempty"`

	// AWS region of the ECR registry, e.g. eu-west-3.
	//+kubebuilder:validation:MinLength=1
	Region string `json:"region"`
}
