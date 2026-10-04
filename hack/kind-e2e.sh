#!/usr/bin/env bash
# End-to-end install check: build the image, install the chart on a kind
# cluster with the mock backend, and require a NodeHealthReport to appear.
#
#   hack/kind-e2e.sh            create a cluster, test, delete it
#   KEEP=1 hack/kind-e2e.sh     leave the cluster running afterwards
#
# Needs docker, kind, kubectl and helm.
set -euo pipefail

CLUSTER="${CLUSTER:-tropis-e2e}"
NS=tropis-system
IMAGE=tropis:e2e
CTX="kind-$CLUSTER"
K="kubectl --context $CTX"

cleanup() {
	if [ "${KEEP:-}" != 1 ]; then
		kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT

step() { echo; echo "==> $*"; }

step "building $IMAGE"
docker build -q --build-arg VERSION=e2e -t "$IMAGE" .

step "creating kind cluster $CLUSTER"
kind create cluster --name "$CLUSTER" --wait 180s
kind load docker-image "$IMAGE" --name "$CLUSTER"

step "installing the chart with the mock backend"
start=$(date +%s)
helm install tropis deploy/helm/tropis --kube-context "$CTX" \
	--namespace "$NS" --create-namespace \
	--set image.repository="${IMAGE%%:*}" --set image.tag="${IMAGE##*:}" --set image.pullPolicy=Never \
	--set backend.provider=mock --set sweep.all=true \
	--wait --timeout 5m

step "waiting for the first verdict"
node="$($K get nodes -o jsonpath='{.items[0].metadata.name}')"
for _ in $(seq 1 60); do
	rel="$($K get nodehealthreport "$node" -o jsonpath='{.status.relationship}' 2>/dev/null || true)"
	[ -n "$rel" ] && break
	sleep 5
done
[ -n "$rel" ] || { echo "no NodeHealthReport for $node"; $K -n "$NS" logs job/tropis-sweep-initial || true; exit 1; }
echo "first verdict for $node after $(($(date +%s) - start))s: $rel"
$K get nodehealthreports -o wide

step "the report carries a complete verdict"
for field in relationship confidence inputDigest backend.promptVersion evidence; do
	v="$($K get nodehealthreport "$node" -o jsonpath="{.status.$field}")"
	[ -n "$v" ] || { echo "status.$field is empty"; exit 1; }
done
echo ok

step "helm test"
helm test tropis --kube-context "$CTX" --namespace "$NS" --timeout 3m

step "RBAC, as the API server enforces it"
SA="--as=system:serviceaccount:$NS:tropis-analyzer"
must() { [ "$($K auth can-i "$@" "$SA" 2>/dev/null)" = "$want" ] || { echo "expected '$want' for: $*"; exit 1; }; }
want=no
must delete pods -A
must patch nodes
must create pods --subresource=eviction -n default
must create pods --subresource=exec -n "$NS"
must get secrets -n "$NS"
must get pods --subresource=proxy -n kube-system
# Kubernetes Events are an opt-in; a default install cannot create them.
must create events.events.k8s.io -n default
want=yes
must get pods --subresource=proxy -n "$NS"
must update nodehealthreports.tropis.io --subresource=status
echo ok

step "the collector has no API token and no host namespaces"
spec="$($K -n "$NS" get ds tropis-collector -o jsonpath='{.spec.template.spec.automountServiceAccountToken}|{.spec.template.spec.hostPID}|{.spec.template.spec.hostNetwork}')"
[ "$spec" = "false||" ] || { echo "collector spec: $spec"; exit 1; }
if $K -n "$NS" exec ds/tropis-collector -- test -e /var/run/secrets/kubernetes.io/serviceaccount/token 2>/dev/null; then
	echo "the collector has a service account token"
	exit 1
fi
echo ok

step "opt-in node events: enable, force a change, find the event"
helm upgrade tropis deploy/helm/tropis --kube-context "$CTX" --namespace "$NS" --reuse-values \
	--set notifications.events.enabled=true --set notifications.on=any --wait --timeout 5m >/dev/null
want=yes
must create events.events.k8s.io -n default
want=no
must create events.events.k8s.io -n kube-system
# Remove the report so the next sweep sees a first verdict, which the "any"
# policy notifies.
$K delete nodehealthreport "$node" >/dev/null
$K -n "$NS" create job --from=cronjob/tropis-sweep tropis-e2e-events >/dev/null
$K -n "$NS" wait --for=condition=complete job/tropis-e2e-events --timeout=180s >/dev/null
reason="$($K -n default get events.events.k8s.io --field-selector "regarding.name=$node" -o jsonpath="{.items[?(@.reportingController==\"tropis.io/sweep\")].reason}")"
[ -n "$reason" ] || { echo "no Tropis event on $node"; $K -n "$NS" logs job/tropis-e2e-events; exit 1; }
echo "event on $node: $reason"

step "capture a fixture from the live node and replay it"
# A scenario with no injected fault, captured from the live node: this checks
# the capture-to-score loop end to end, not any diagnosis.
work="$(mktemp -d)"
cat >"$work/inventory.yaml" <<EOF
apiVersion: tropis.io/v1alpha1
kind: Inventory
cluster: {name: $CLUSTER, kubernetesVersion: unknown}
nodes:
  - name: $node
    role: control-plane
    disks: []
EOF
kubeconfig="$work/kubeconfig"
kind get kubeconfig --name "$CLUSTER" >"$kubeconfig"
go run ./cmd/tropis capture --kubeconfig "$kubeconfig" --namespace "$NS" \
	--scenario stable-defects-config-crash --variant npd-absent \
	--node "$node" --inventory "$work/inventory.yaml" --out "$work/corpus" --alerts-file /dev/null
go run ./cmd/tropis eval --corpus "$work/corpus" --backend mock --out "$work/results" --quiet
grep -q '"synthetic": false' "$work/results/results.json" || { echo "the capture was not recorded as real"; exit 1; }
rm -rf "$work"
echo ok

echo
echo "kind end-to-end check passed"
