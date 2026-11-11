package controllers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"time"

	awsv1alpha2 "github.com/panroman14/kube-ecr-secrets-operator/api/v1alpha2"
	"github.com/panroman14/kube-ecr-secrets-operator/common"

	awsConfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/smithy-go"
	v1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	ReadyCondition = "Ready"

	ReasonSecretsUpdated     = "SecretsUpdated"
	ReasonCredentialsInvalid = "CredentialsInvalid"
	ReasonAWSRequestFailed   = "AWSRequestFailed"
	ReasonSecretConflict     = "SecretConflict"
	ReasonSecretWriteFailed  = "SecretWriteFailed"

	expiryAnnotation = "expiry"

	// ECR tokens live 12h. The pull secrets are rewritten every refreshInterval, so a
	// deleted pull secret, a recreated namespace or rotated AWS keys are picked up within
	// that time, and a token always has 11h left. refreshBeforeExpiry caps it for
	// tokens that live shorter.
	refreshInterval     = time.Hour
	refreshBeforeExpiry = 2 * time.Hour
	minRequeue          = time.Minute
	awsTimeout          = 30 * time.Second
	// Retry delay for errors a retry alone will not fix (missing credentials, foreign
	// secret in the way). Other errors use the controller-runtime backoff.
	slowRetry = 5 * time.Minute
)

// errNeedsAction marks errors that need a user action; they are retried slowly.
var errNeedsAction = errors.New("needs user action")

type DockerServerAuthInfo struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Auth     string `json:"auth"`
}

type DockerAuthConfig struct {
	Auths map[string]DockerServerAuthInfo `json:"auths"`
}

// pullSecretSource is the common view of AWSECRImagePullSecret and ClusterAWSECRImagePullSecret.
type pullSecretSource interface {
	client.Object
	gvk() schema.GroupVersionKind
	// object is the underlying API object (the wrapper itself is not a registered type).
	object() client.Object
	access() common.AWSAccess
	pullSecretName() string
	targetNamespaces() []string
	conditions() *[]metav1.Condition
	// managedNamespaces / setManagedNamespaces track where pull secrets were written,
	// to clean up namespaces removed from the spec (cluster object only).
	managedNamespaces() []string
	setManagedNamespaces([]string)
}

type namespacedSource struct {
	*awsv1alpha2.AWSECRImagePullSecret
}

func (s namespacedSource) gvk() schema.GroupVersionKind {
	return awsv1alpha2.GroupVersion.WithKind("AWSECRImagePullSecret")
}
func (s namespacedSource) object() client.Object { return s.AWSECRImagePullSecret }
func (s namespacedSource) access() common.AWSAccess {
	a := s.Spec.AWSAccess
	a.SecretNamespace = s.Namespace
	return a
}
func (s namespacedSource) pullSecretName() string          { return s.Spec.SecretName }
func (s namespacedSource) targetNamespaces() []string      { return []string{s.Namespace} }
func (s namespacedSource) conditions() *[]metav1.Condition { return &s.Status.Conditions }
func (s namespacedSource) managedNamespaces() []string     { return nil }
func (s namespacedSource) setManagedNamespaces([]string)   {}

type clusterSource struct {
	*awsv1alpha2.ClusterAWSECRImagePullSecret
}

func (s clusterSource) gvk() schema.GroupVersionKind {
	return awsv1alpha2.GroupVersion.WithKind("ClusterAWSECRImagePullSecret")
}
func (s clusterSource) object() client.Object            { return s.ClusterAWSECRImagePullSecret }
func (s clusterSource) access() common.AWSAccess         { return s.Spec.AWSAccess }
func (s clusterSource) pullSecretName() string           { return s.Spec.SecretName }
func (s clusterSource) targetNamespaces() []string       { return s.Spec.Namespaces }
func (s clusterSource) conditions() *[]metav1.Condition  { return &s.Status.Conditions }
func (s clusterSource) managedNamespaces() []string      { return s.Status.Namespaces }
func (s clusterSource) setManagedNamespaces(ns []string) { s.Status.Namespaces = ns }

// changePredicate reacts to spec changes (generation) and annotation changes, not to the
// status updates the controller writes itself. Changing an annotation is a way to force
// an immediate refresh, e.g. after rotating the AWS keys.
type changePredicate struct {
	predicate.Funcs
}

func (changePredicate) Update(e event.UpdateEvent) bool {
	return e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() ||
		!reflect.DeepEqual(e.ObjectOld.GetAnnotations(), e.ObjectNew.GetAnnotations())
}

type GenericAWSECRPullSecretReconciler[reconciled awsv1alpha2.AWSECRImagePullSecret | awsv1alpha2.ClusterAWSECRImagePullSecret] struct {
	client.Client
	Recorder record.EventRecorder
}

// SetupWithManager sets up the controller with the Manager.
func (r *GenericAWSECRPullSecretReconciler[a]) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr)
	switch reflect.TypeFor[a]() {
	case reflect.TypeOf(awsv1alpha2.AWSECRImagePullSecret{}):
		b = b.For(&awsv1alpha2.AWSECRImagePullSecret{})
	case reflect.TypeOf(awsv1alpha2.ClusterAWSECRImagePullSecret{}):
		b = b.For(&awsv1alpha2.ClusterAWSECRImagePullSecret{})
	}
	return b.WithEventFilter(changePredicate{}).Complete(r)
}

func (r *GenericAWSECRPullSecretReconciler[a]) load(ctx context.Context, req ctrl.Request) (pullSecretSource, error) {
	if reflect.TypeFor[a]() == reflect.TypeOf(awsv1alpha2.AWSECRImagePullSecret{}) {
		obj := &awsv1alpha2.AWSECRImagePullSecret{}
		if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
			return nil, err
		}
		return namespacedSource{obj}, nil
	}
	obj := &awsv1alpha2.ClusterAWSECRImagePullSecret{}
	if err := r.Get(ctx, client.ObjectKey{Name: req.Name}, obj); err != nil {
		return nil, err
	}
	return clusterSource{obj}, nil
}

//+kubebuilder:rbac:groups=aws.zakariaamine.com,resources=awsecrimagepullsecrets;clusterawsecrimagepullsecrets,verbs=get;list;watch
//+kubebuilder:rbac:groups=aws.zakariaamine.com,resources=awsecrimagepullsecrets/status;clusterawsecrimagepullsecrets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile requests an ECR token with the referenced AWS credentials, writes it as a
// dockerconfigjson pull secret into the target namespaces, and requeues itself to rewrite
// them every refreshInterval.
func (r *GenericAWSECRPullSecretReconciler[a]) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	src, err := r.load(ctx, req)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// Deleted. The pull secrets go away through their owner references.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	// Snapshot before refresh changes the status, so the status patch includes every change.
	base, ok := src.object().DeepCopyObject().(client.Object)
	if !ok {
		return ctrl.Result{}, errors.New("deepcopy failed")
	}

	expiresAt, reason, err := r.refresh(ctx, src)
	if err != nil {
		r.Recorder.Event(src.object(), v1.EventTypeWarning, reason, err.Error())
		if statusErr := r.setReady(ctx, src, base, metav1.ConditionFalse, reason, err.Error()); statusErr != nil {
			logger.Error(statusErr, "status update failed")
		}
		if errors.Is(err, errNeedsAction) {
			logger.Info("waiting for user action", "reason", reason, "error", err.Error())
			return ctrl.Result{RequeueAfter: slowRetry}, nil
		}
		return ctrl.Result{}, err
	}

	next := time.Now().Add(refreshInterval)
	if capAt := expiresAt.Add(-refreshBeforeExpiry); capAt.Before(next) {
		next = capAt
	}
	msg := fmt.Sprintf("pull secret %s updated in %v; token expires %s, next refresh %s",
		src.pullSecretName(), src.targetNamespaces(), expiresAt.UTC().Format(time.RFC3339), next.UTC().Format(time.RFC3339))
	wasReady := meta.IsStatusConditionTrue(*src.conditions(), ReadyCondition)
	if err := r.setReady(ctx, src, base, metav1.ConditionTrue, ReasonSecretsUpdated, msg); err != nil {
		return ctrl.Result{}, err
	}
	// One event when it becomes ready, not one per hourly refresh.
	if !wasReady {
		r.Recorder.Event(src.object(), v1.EventTypeNormal, ReasonSecretsUpdated, msg)
	}
	logger.Info("pull secrets updated", "namespaces", src.targetNamespaces(), "nextRefresh", next)

	return ctrl.Result{RequeueAfter: max(time.Until(next), minRequeue)}, nil
}

// refresh does the work and returns the token expiry, or the condition reason and the error.
func (r *GenericAWSECRPullSecretReconciler[a]) refresh(ctx context.Context, src pullSecretSource) (time.Time, string, error) {
	access := src.access()

	accessKeyID, secretAccessKey, err := r.readCredentials(ctx, access)
	if err != nil {
		return time.Time{}, ReasonCredentialsInvalid, err
	}

	dockerConfig, expiresAt, err := getDockerJSONConfigFromAWS(ctx, accessKeyID, secretAccessKey, access.Region)
	if err != nil {
		return time.Time{}, ReasonAWSRequestFailed, err
	}

	// Write every namespace even if one fails, so one bad namespace does not leave the
	// others with an expiring token. The first failure gives the condition reason.
	var errs []error
	firstReason := ""
	fail := func(reason string, err error) {
		if firstReason == "" {
			firstReason = reason
		}
		errs = append(errs, err)
	}
	for _, ns := range src.targetNamespaces() {
		if reason, err := r.writePullSecret(ctx, src, ns, dockerConfig, expiresAt); err != nil {
			fail(reason, fmt.Errorf("namespace %s: %w", ns, err))
		}
	}

	// Remove the pull secret from namespaces dropped from the spec. A namespace whose
	// cleanup fails stays in the list and is retried.
	managed := append([]string{}, src.targetNamespaces()...)
	for _, ns := range src.managedNamespaces() {
		if slices.Contains(src.targetNamespaces(), ns) {
			continue
		}
		if err := r.deletePullSecret(ctx, src, ns); err != nil {
			managed = append(managed, ns)
			fail(ReasonSecretWriteFailed, fmt.Errorf("namespace %s: removing old pull secret: %w", ns, err))
		}
	}
	src.setManagedNamespaces(managed)

	if len(errs) > 0 {
		return time.Time{}, firstReason, errors.Join(errs...)
	}
	return expiresAt, "", nil
}

// deletePullSecret deletes the pull secret in namespace if this object created it.
func (r *GenericAWSECRPullSecretReconciler[a]) deletePullSecret(ctx context.Context, src pullSecretSource, namespace string) error {
	sec := &v1.Secret{}
	err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: src.pullSecretName()}, sec)
	switch {
	case apiErrors.IsNotFound(err):
		return nil
	case apiErrors.IsForbidden(err):
		// The namespace was also removed from secretNamespaces: nothing we can do.
		log.FromContext(ctx).Info("no access to remove old pull secret, leaving it",
			"namespace", namespace, "secret", src.pullSecretName())
		return nil
	case err != nil:
		return err
	case !ownedBy(sec, src):
		return nil // not ours: leave it alone
	}
	err = r.Delete(ctx, sec, client.Preconditions{UID: &sec.UID})
	if apiErrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (r *GenericAWSECRPullSecretReconciler[a]) readCredentials(ctx context.Context, access common.AWSAccess) (string, string, error) {
	sec := &v1.Secret{}
	key := client.ObjectKey{Namespace: access.SecretNamespace, Name: access.SecretName}
	if err := r.Get(ctx, key, sec); err != nil {
		switch {
		case apiErrors.IsNotFound(err):
			return "", "", fmt.Errorf("%w: AWS credentials secret %s not found", errNeedsAction, key)
		case apiErrors.IsForbidden(err):
			return "", "", fmt.Errorf("%w: no access to AWS credentials secret %s; add namespace %s to the chart value secretNamespaces",
				errNeedsAction, key, access.SecretNamespace)
		}
		return "", "", err
	}
	id := strings.TrimSpace(string(sec.Data[common.AccessKeyIDKey]))
	secret := strings.TrimSpace(string(sec.Data[common.SecretAccessKeyKey]))
	if id == "" || secret == "" {
		return "", "", fmt.Errorf("%w: secret %s must contain the keys %s and %s",
			errNeedsAction, key, common.AccessKeyIDKey, common.SecretAccessKeyKey)
	}
	return id, secret, nil
}

// writePullSecret creates the pull secret, or updates it only if this object created it.
func (r *GenericAWSECRPullSecretReconciler[a]) writePullSecret(ctx context.Context, src pullSecretSource, namespace string,
	dockerConfig []byte, expiresAt time.Time) (string, error) {
	noAccess := fmt.Errorf("%w: no access to secrets in namespace %s; add it to the chart value secretNamespaces",
		errNeedsAction, namespace)

	existing := &v1.Secret{}
	err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: src.pullSecretName()}, existing)
	switch {
	case apiErrors.IsNotFound(err):
		if err := r.Create(ctx, newDockerSecret(src, namespace, dockerConfig, expiresAt)); err != nil {
			switch {
			case apiErrors.IsNotFound(err):
				return ReasonSecretWriteFailed, fmt.Errorf("%w: namespace %s does not exist", errNeedsAction, namespace)
			case apiErrors.IsForbidden(err):
				return ReasonSecretWriteFailed, noAccess
			}
			return ReasonSecretWriteFailed, err
		}
		return "", nil
	case apiErrors.IsForbidden(err):
		return ReasonSecretWriteFailed, noAccess
	case err != nil:
		return ReasonSecretWriteFailed, err
	}

	// Never take over a secret this object did not create: the operator can write
	// secrets, and a typo in secretName must not overwrite e.g. a TLS or app secret.
	if !ownedBy(existing, src) {
		return ReasonSecretConflict, fmt.Errorf("%w: secret %s already exists and was not created by %s %s; "+
			"use another secretName or delete that secret", errNeedsAction, src.pullSecretName(), src.gvk().Kind, src.GetName())
	}
	if existing.Type != v1.SecretTypeDockerConfigJson {
		return ReasonSecretConflict, fmt.Errorf("%w: secret %s has type %s, expected %s",
			errNeedsAction, src.pullSecretName(), existing.Type, v1.SecretTypeDockerConfigJson)
	}

	if existing.Data == nil {
		existing.Data = map[string][]byte{}
	}
	existing.Data[v1.DockerConfigJsonKey] = dockerConfig
	if existing.Annotations == nil {
		existing.Annotations = map[string]string{}
	}
	existing.Annotations[expiryAnnotation] = expiresAt.UTC().Format(time.RFC3339)
	if err := r.Update(ctx, existing); err != nil {
		return ReasonSecretWriteFailed, err
	}
	return "", nil
}

func ownedBy(sec *v1.Secret, owner client.Object) bool {
	for _, ref := range sec.OwnerReferences {
		if ref.UID == owner.GetUID() {
			return true
		}
	}
	return false
}

func (r *GenericAWSECRPullSecretReconciler[a]) setReady(ctx context.Context, src pullSecretSource, base client.Object,
	status metav1.ConditionStatus, reason, message string) error {
	meta.SetStatusCondition(src.conditions(), metav1.Condition{
		Type:               ReadyCondition,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: src.GetGeneration(),
	})
	return r.Status().Patch(ctx, src.object(), client.MergeFrom(base))
}

// getDockerJSONConfigFromAWS returns a dockerconfigjson for the account's ECR registry
// in the region, and the token expiry.
func getDockerJSONConfigFromAWS(ctx context.Context, accessKeyID, secretAccessKey, region string) ([]byte, time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, awsTimeout)
	defer cancel()
	cfg, err := awsConfig.LoadDefaultConfig(ctx,
		awsConfig.WithRegion(region),
		awsConfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
	)
	if err != nil {
		return nil, time.Time{}, err
	}

	out, err := ecr.NewFromConfig(cfg).GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		// Wrong keys or missing permission will not fix themselves: retry slowly.
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "UnrecognizedClientException", "InvalidSignatureException", "AccessDeniedException",
				"AccessDenied", "InvalidClientTokenId", "SignatureDoesNotMatch":
				return nil, time.Time{}, fmt.Errorf("%w: ecr:GetAuthorizationToken: %s: %s",
					errNeedsAction, apiErr.ErrorCode(), apiErr.ErrorMessage())
			}
		}
		return nil, time.Time{}, fmt.Errorf("ecr:GetAuthorizationToken: %w", err)
	}
	if len(out.AuthorizationData) == 0 || out.AuthorizationData[0].AuthorizationToken == nil ||
		out.AuthorizationData[0].ProxyEndpoint == nil || out.AuthorizationData[0].ExpiresAt == nil {
		return nil, time.Time{}, errors.New("ecr:GetAuthorizationToken returned no authorization data")
	}
	data := out.AuthorizationData[0]

	endpoint, err := url.Parse(*data.ProxyEndpoint)
	if err != nil {
		return nil, time.Time{}, err
	}
	decoded, err := base64.StdEncoding.DecodeString(*data.AuthorizationToken)
	if err != nil {
		return nil, time.Time{}, err
	}
	user, password, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return nil, time.Time{}, errors.New("unexpected ECR authorization token format")
	}

	cfgJSON, err := json.Marshal(DockerAuthConfig{
		Auths: map[string]DockerServerAuthInfo{
			endpoint.Hostname(): {Username: user, Password: password, Auth: *data.AuthorizationToken},
		},
	})
	return cfgJSON, *data.ExpiresAt, err
}

// newDockerSecret builds the pull secret owned by src: deleting src deletes it.
func newDockerSecret(src pullSecretSource, namespace string, dockerConfig []byte, expiresAt time.Time) *v1.Secret {
	gvk := src.gvk()
	return &v1.Secret{
		Type: v1.SecretTypeDockerConfigJson,
		ObjectMeta: metav1.ObjectMeta{
			Name:      src.pullSecretName(),
			Namespace: namespace,
			Annotations: map[string]string{
				expiryAnnotation: expiresAt.UTC().Format(time.RFC3339),
			},
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kube-ecr-secrets-operator",
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: gvk.GroupVersion().String(),
				Kind:       gvk.Kind,
				Name:       src.GetName(),
				UID:        src.GetUID(),
			}},
		},
		Data: map[string][]byte{v1.DockerConfigJsonKey: dockerConfig},
	}
}
