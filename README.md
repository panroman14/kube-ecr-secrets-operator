# kube-ecr-secrets-operator

Keeps AWS ECR image pull secrets fresh in Kubernetes. ECR tokens expire after 12 hours, so a
static `imagePullSecret` breaks the next time a pod is rescheduled. This operator requests a
token with an IAM user's keys, writes a `kubernetes.io/dockerconfigjson` Secret into the
namespaces you list, and rewrites it every hour (the token lives 12h).

Hardened fork of [zak905/kube-ecr-secrets-operator](https://github.com/zak905/kube-ecr-secrets-operator) 0.2.13.
The API group and the `v1alpha2` kinds are unchanged.

## Supported Kubernetes versions

Requires **1.25+** (CRD validation rules, CEL). Tested end to end (real ECR pull) on k3s:

| Kubernetes | Result |
|---|---|
| 1.25.16 | ok |
| 1.27.16 | ok |
| 1.34.4  | ok |

## How it works

1. You create a Secret with the IAM user's `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`.
2. You create a `ClusterAWSECRImagePullSecret` (or a namespaced `AWSECRImagePullSecret`) that points to it.
3. The operator calls `ecr:GetAuthorizationToken` and creates the pull secret in each target namespace.
4. It rewrites the pull secrets every hour. A deleted pull secret, a recreated namespace or rotated AWS keys are picked up within an hour (or right away, see [Status](#status-and-troubleshooting)).
5. Deleting the object deletes its pull secrets (owner references). Removing a namespace from `namespaces` deletes the pull secret there.

The token covers the ECR registry of the IAM user's own AWS account, in the given region.

The keys never appear in the custom object, only in a Secret.

## Install

```bash
helm install kube-ecr-secrets-operator ./charts/kube-ecr-secrets-operator \
  --kube-context your-context -n kube-ecr-secrets-operator --create-namespace \
  --set secretNamespaces='{default}'
```

The chart ships the CRDs in `crds/`. Helm installs them but **does not upgrade** them, so after
a chart upgrade run:

```bash
kubectl --context your-context apply --server-side --force-conflicts -f charts/kube-ecr-secrets-operator/crds/
```

## Configuration

### IAM user

Minimum policy (one repository; widen `Resource` as needed):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow", "Action": "ecr:GetAuthorizationToken", "Resource": "*" },
    { "Effect": "Allow",
      "Action": ["ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer", "ecr:BatchCheckLayerAvailability"],
      "Resource": "arn:aws:ecr:eu-west-1:123456789012:repository/your-repository" }
  ]
}
```

### Credentials Secret

```bash
kubectl --context your-context -n default create secret generic aws-ecr-credentials --from-env-file=aws.env
```

`aws.env` contains the two lines `AWS_ACCESS_KEY_ID=...` and `AWS_SECRET_ACCESS_KEY=...`.

### ClusterAWSECRImagePullSecret (several namespaces)

```yaml
apiVersion: aws.zakariaamine.com/v1alpha2
kind: ClusterAWSECRImagePullSecret
metadata:
  name: ecr
spec:
  awsAccess:
    secretName: aws-ecr-credentials   # Secret with the keys
    secretNamespace: default          # required here
    region: eu-west-1
  secretName: ecr-pull                # pull secret created in every namespace below
  namespaces: [default, app]
```

### AWSECRImagePullSecret (own namespace)

```yaml
apiVersion: aws.zakariaamine.com/v1alpha2
kind: AWSECRImagePullSecret
metadata:
  name: ecr
  namespace: app
spec:
  awsAccess:
    secretName: aws-ecr-credentials   # must be in the same namespace
    region: eu-west-1
  secretName: ecr-pull
```

Then use `imagePullSecrets: [{name: ecr-pull}]` in pods. More files are in [examples/](examples/).

The API server enforces these rules with CEL:
- `secretName` cannot be changed after creation. Delete and recreate the object instead.
- A cluster object must set `awsAccess.secretNamespace`. A namespaced object must not set it.

### Objects from Helm values

Instead of writing objects by hand, the chart can create `ClusterAWSECRImagePullSecret` objects:

```yaml
secretNamespaces: [default]     # credentials namespace + every target namespace
pullSecrets:
  - name: ecr
    region: eu-west-1
    credentialsSecret: aws-ecr-credentials
    credentialsNamespace: default
    secretName: ecr-pull
    namespaces: [default]
```

### Chart values

| Value | Default | Meaning |
|---|---|---|
| `image.repository` | `docker.io/sailorpapay/kube-ecr-secrets-operator` | Operator image (public on Docker Hub) |
| `image.tag` | chart `appVersion` | Image tag |
| `image.digest` | `""` | If set, used instead of the tag |
| `secretNamespaces` | `[]` | Namespaces where the operator may read/write Secrets. Empty = all namespaces. Each one must exist before `helm install/upgrade` (the chart creates a Role in it). If such a namespace is deleted and recreated, run `helm upgrade` again to recreate the Role |
| `pullSecrets` | `[]` | Objects to create, see above. To remove all of them with `--reuse-values`, pass `--set-json 'pullSecrets=[]'` (`=null` keeps the old list) |
| `metricsBindAddress` | `"0"` | `"0"` disables metrics (the endpoint has no auth); e.g. `":8080"` |
| `resources`, `nodeSelector`, `tolerations`, `affinity`, `priorityClassName`, `podAnnotations` | | Usual pod settings |

## Status and troubleshooting

```bash
kubectl --context your-context get clusterawsecrimagepullsecrets
kubectl --context your-context describe clusterawsecrimagepullsecret ecr
```

`Ready=True` / `SecretsUpdated` shows when the token expires and when the next refresh happens.
If some namespaces fail, the others are still written and the message lists every failed namespace.
The operator retries errors that need your action every 5 minutes:

| Reason | Cause |
|---|---|
| `CredentialsInvalid` | Credentials Secret missing, missing a key, or its namespace is not in `secretNamespaces` |
| `AWSRequestFailed` | AWS rejected the keys or the IAM user lacks `ecr:GetAuthorizationToken` |
| `SecretConflict` | A Secret with that name already exists and was not created by this object. The operator does not overwrite it |

To refresh immediately, change any annotation on the object:

```bash
kubectl --context your-context annotate clusterawsecrimagepullsecret ecr refresh="$(date +%s)" --overwrite
```

## Security

- Runs as non-root (65532) with a read-only root filesystem, all capabilities dropped and seccomp `RuntimeDefault`, from a distroless image.
- No webhook, no certificates, no Service. Only the health probe listens, on `:8081`.
- Secret access is `get/create/update/patch/delete`, with no `list`/`watch` because Secrets are not cached. Use `secretNamespaces` to limit access to specific namespaces.
- The operator only updates or deletes Secrets it created itself, checked by owner UID.

## Changes from upstream

| Removed / changed | Why |
|---|---|
| Mutating webhook, cert job, cert-manager option, Service | The webhook only converted the deprecated `v1alpha1` object and validated credentials. CEL rules now do the validation, with no TLS setup |
| `v1alpha1` `AWSECRCredential` and its controller | Converting deletes the old object, and garbage collection then removes the pull secrets it owned. Old-format objects converted to empty keys |
| Keys inline in the object | Moved to a referenced Secret |
| Overwriting any Secret with the target name | Now a `SecretConflict` status |
| Cluster-wide `list/watch` on Secrets | Not needed. Secrets are read directly, without a cache |
| Refresh only near expiry | Hourly, so a lost pull secret comes back quickly. One failing namespace no longer blocks the others |
| Metrics on by default | Off by default (no auth on the endpoint) |
| Silent failures | Status conditions with reasons, plus events |

## Development

```bash
make build          # generate, fmt, vet, build bin/manager
make generate       # regenerate DeepCopy and chart CRDs after editing api/v1alpha2
make helm-lint
make docker-build IMG=docker.io/your-username/kube-ecr-secrets-operator:0.1.0
make docker-push  IMG=docker.io/your-username/kube-ecr-secrets-operator:0.1.0
```

License: Apache 2.0, same as upstream.
