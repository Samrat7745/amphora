// Package chart renders the Helm chart of the Go service with `helm template` and asserts on the manifests.
package chart

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const chartDir = "../../amphora-service/charts/amphora"

type obj map[string]any

func (o obj) get(path ...string) any {
	var cur any = map[string]any(o)
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

// render runs helm template and returns the documents by kind.
func render(t *testing.T, args ...string) (map[string]obj, string, error) {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not on PATH")
	}
	dir, _ := filepath.Abs(chartDir)
	cmd := exec.Command(helm, append([]string{"template", "rel", dir, "--namespace", "ns"}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, errb.String(), err
	}
	docs := map[string]obj{}
	for _, d := range strings.Split(out.String(), "\n---") {
		if strings.TrimSpace(d) == "" {
			continue
		}
		var o obj
		if err := yaml.Unmarshal([]byte(d), &o); err != nil {
			t.Fatalf("invalid YAML: %v\n%s", err, d)
		}
		if k, _ := o["kind"].(string); k != "" {
			docs[k] = o
		}
	}
	return docs, "", nil
}

func mustRender(t *testing.T, args ...string) map[string]obj {
	t.Helper()
	docs, stderr, err := render(t, args...)
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, stderr)
	}
	return docs
}

func container(t *testing.T, docs map[string]obj) map[string]any {
	t.Helper()
	cs, _ := docs["Deployment"].get("spec", "template", "spec", "containers").([]any)
	if len(cs) != 1 {
		t.Fatalf("want one container, got %v", cs)
	}
	return cs[0].(map[string]any)
}

// env returns the plain values and flags the secret references.
func env(t *testing.T, c map[string]any) (vals map[string]any, refs map[string]map[string]any) {
	vals, refs = map[string]any{}, map[string]map[string]any{}
	for _, e := range c["env"].([]any) {
		m := e.(map[string]any)
		name := m["name"].(string)
		if _, dup := vals[name]; dup {
			t.Fatalf("duplicate env %s", name)
		}
		if vf, ok := m["valueFrom"].(map[string]any); ok {
			refs[name] = vf["secretKeyRef"].(map[string]any)
			vals[name] = nil
			continue
		}
		vals[name] = m["value"]
	}
	return
}

func TestDefaultRender(t *testing.T) {
	docs := mustRender(t)
	if len(docs) != 2 || docs["Deployment"] == nil || docs["Service"] == nil {
		t.Fatalf("kinds: %v", docs)
	}
	c := container(t, docs)
	vals, refs := env(t, c)
	want := map[string]any{
		"AMPHORA_VC_PARTNERS": "", "AMPHORA_OPENING_TIMEOUT": "5000", "PLAYER_ID": "0", "NO_SSL_VALIDATION": "false",
		"TRUSTED_CERTIFICATES": nil, "SPRING_PROFILES_ACTIVE": "k8s", "CASTOR_SERVICE_URI": "castor.default.svc.cluster.local",
		"AMPHORA_USER_ID_FIELD_NAME": "sub", "AMPHORA_OPA_DEFAULT_POLICY_PACKAGE": "carbynestack.def",
		"AMPHORA_OPA_URL": "http://opa.default.svc.cluster.local:8081/", "MAC_KEY": "", "SPDZ_PRIME": "", "SPDZ_R": "", "SPDZ_R_INV": "",
		"REDIS_HOST": "redis.default.svc.cluster.local", "REDIS_PORT": "6379", "MINIO_ENDPOINT": "http://minio.default.svc.cluster.local:9000",
		"DB_HOST": "dbms-repl.default.svc.cluster.local", "DB_PORT": "5432", "DB_NAME": "amphora", "COMPAT_JAVA_TAG_REPLACE_BUG": "false",
		"LOG_LEVEL": "INFO", "DB_USER": nil, "DB_PASSWORD": nil,
	}
	for k, v := range want {
		got, ok := vals[k]
		if !ok || got != v {
			t.Errorf("env %s = %#v (present=%v), want %#v", k, got, ok, v)
		}
	}
	if len(vals) != len(want) {
		t.Errorf("unexpected env set: %d variables, want %d", len(vals), len(want))
	}
	if refs["DB_USER"]["key"] != "username" || refs["DB_PASSWORD"]["key"] != "password" {
		t.Errorf("secret refs: %v", refs)
	}
	if c["image"] != "ghcr.io/carbynestack/amphora-service:latest" || c["imagePullPolicy"] != "IfNotPresent" {
		t.Errorf("image %v %v", c["image"], c["imagePullPolicy"])
	}
	ports := c["ports"].([]any)
	if len(ports) != 1 || ports[0].(map[string]any)["containerPort"] != float64(10000) || ports[0].(map[string]any)["name"] != "mgmt" {
		t.Errorf("ports: %v", ports)
	}
	// hardened security context
	sc := obj(c["securityContext"].(map[string]any))
	if sc.get("allowPrivilegeEscalation") != false || sc.get("readOnlyRootFilesystem") != true || sc.get("runAsNonRoot") != true ||
		sc.get("runAsUser") != float64(1000) || sc.get("seccompProfile", "type") != "RuntimeDefault" {
		t.Errorf("securityContext: %v", sc)
	}
	if drop, _ := sc.get("capabilities", "drop").([]any); len(drop) != 1 || drop[0] != "ALL" {
		t.Errorf("capabilities: %v", sc["capabilities"])
	}
	// probes
	live, ready := obj(c["livenessProbe"].(map[string]any)), obj(c["readinessProbe"].(map[string]any))
	if live.get("httpGet", "path") != "/actuator/health" || live.get("httpGet", "port") != float64(10000) ||
		live["initialDelaySeconds"] != float64(60) || live["periodSeconds"] != float64(10) || live["failureThreshold"] != float64(3) {
		t.Errorf("liveness: %v", live)
	}
	if ready["initialDelaySeconds"] != float64(0) || ready["periodSeconds"] != float64(5) || ready["failureThreshold"] != float64(3) {
		t.Errorf("readiness: %v", ready)
	}
	// service
	svc := docs["Service"]
	if svc.get("spec", "type") != "ClusterIP" || svc.get("metadata", "namespace") != "ns" || svc.get("metadata", "annotations") != nil {
		t.Errorf("service: %v", svc)
	}
	p := svc.get("spec", "ports").([]any)[0].(map[string]any)
	if p["port"] != float64(10000) || p["targetPort"] != float64(10000) || p["name"] != "http" {
		t.Errorf("service port: %v", p)
	}
	if docs["Deployment"].get("spec", "replicas") != float64(1) {
		t.Error("replicas")
	}
	// the selector of the service matches the pod labels of the deployment
	sel := svc.get("spec", "selector").(map[string]any)
	labels := docs["Deployment"].get("spec", "template", "metadata", "labels").(map[string]any)
	for k, v := range sel {
		if labels[k] != v {
			t.Errorf("selector %s=%v not in pod labels %v", k, v, labels)
		}
	}
}

func TestCompatJavaTagReplaceBug(t *testing.T) {
	for _, tc := range []struct{ set, want string }{{"true", "true"}, {"false", "false"}} {
		vals, _ := env(t, container(t, mustRender(t, "--set", "amphora.compatJavaTagReplaceBug="+tc.set)))
		if vals["COMPAT_JAVA_TAG_REPLACE_BUG"] != tc.want {
			t.Errorf("set %s: %v", tc.set, vals["COMPAT_JAVA_TAG_REPLACE_BUG"])
		}
	}
}

func TestDatabaseAndSecretOverrides(t *testing.T) {
	c := container(t, mustRender(t, "--set", "amphora.db.name=custom_db,amphora.db.host=pg,amphora.db.port=6543,amphora.db.userSecretName=us,amphora.db.passwordSecretName=ps"))
	vals, refs := env(t, c)
	if vals["DB_NAME"] != "custom_db" || vals["DB_HOST"] != "pg" || vals["DB_PORT"] != "6543" {
		t.Errorf("%v", vals)
	}
	if refs["DB_USER"]["name"] != "us" || refs["DB_PASSWORD"]["name"] != "ps" {
		t.Errorf("%v", refs)
	}
}

func TestSpdzAndPlayerOverrides(t *testing.T) {
	c := container(t, mustRender(t, "--set", "spdz.macKey=-123,spdz.prime=17,spdz.r=3,spdz.rInv=6,amphora.playerId=2,amphora.openingTimeout=1234,amphora.logLevel=DEBUG,auth.userIdFieldName=preferred_username",
		"--set-string", "amphora.vcPartners[0]=http://a:1,amphora.vcPartners[1]=http://b:2"))
	vals, _ := env(t, c)
	want := map[string]any{"MAC_KEY": "-123", "SPDZ_PRIME": "17", "SPDZ_R": "3", "SPDZ_R_INV": "6", "PLAYER_ID": "2", "AMPHORA_OPENING_TIMEOUT": "1234",
		"LOG_LEVEL": "DEBUG", "AMPHORA_USER_ID_FIELD_NAME": "preferred_username", "AMPHORA_VC_PARTNERS": "http://a:1,http://b:2"}
	for k, v := range want {
		if vals[k] != v {
			t.Errorf("%s = %#v, want %#v", k, vals[k], v)
		}
	}
}

func TestServiceOverrides(t *testing.T) {
	docs := mustRender(t, "--set", "service.type=NodePort,service.port=8080", "--set-string", "service.annotations.prometheus\\.io/scrape=true")
	svc := docs["Service"]
	if svc.get("spec", "type") != "NodePort" {
		t.Errorf("type: %v", svc.get("spec", "type"))
	}
	if svc.get("spec", "ports").([]any)[0].(map[string]any)["port"] != float64(8080) {
		t.Errorf("port")
	}
	if svc.get("metadata", "annotations", "prometheus.io/scrape") != "true" {
		t.Errorf("annotations: %v", svc.get("metadata", "annotations"))
	}
	// the container port is fixed, only the service port changes
	if container(t, docs)["ports"].([]any)[0].(map[string]any)["containerPort"] != float64(10000) {
		t.Error("container port must stay 10000")
	}
}

func TestProbeOverrides(t *testing.T) {
	c := container(t, mustRender(t, "--set", "amphora.probes.liveness.initialDelay=5,amphora.probes.liveness.period=7,amphora.probes.liveness.failureThreshold=9,"+
		"amphora.probes.readiness.initialDelay=1,amphora.probes.readiness.period=2,amphora.probes.readiness.failureThreshold=4"))
	live, ready := c["livenessProbe"].(map[string]any), c["readinessProbe"].(map[string]any)
	if live["initialDelaySeconds"] != float64(5) || live["periodSeconds"] != float64(7) || live["failureThreshold"] != float64(9) {
		t.Errorf("liveness %v", live)
	}
	if ready["initialDelaySeconds"] != float64(1) || ready["periodSeconds"] != float64(2) || ready["failureThreshold"] != float64(4) {
		t.Errorf("readiness %v", ready)
	}
}

func TestImageAndPullSecrets(t *testing.T) {
	docs := mustRender(t, "--set", "amphora.image.registry=reg.io,amphora.image.repository=org/amphora-go,amphora.image.tag=v1.2.3,amphora.image.pullPolicy=Always,amphora.image.pullSecrets[0]=s1,replicaCount=3")
	c := container(t, docs)
	if c["image"] != "reg.io/org/amphora-go:v1.2.3" || c["imagePullPolicy"] != "Always" {
		t.Errorf("%v", c)
	}
	ips, _ := docs["Deployment"].get("spec", "template", "spec", "imagePullSecrets").([]any)
	if len(ips) != 1 || ips[0].(map[string]any)["name"] != "s1" {
		t.Errorf("%v", ips)
	}
	if docs["Deployment"].get("spec", "replicas") != float64(3) {
		t.Error("replicas")
	}
}

func TestKubeVersionConstraint(t *testing.T) {
	if _, stderr, err := render(t, "--kube-version", "1.24.0"); err == nil || !strings.Contains(stderr, "kubeVersion") {
		t.Fatalf("kube 1.24 must be rejected by the chart's kubeVersion constraint: %v %s", err, stderr)
	}
	for _, v := range []string{"1.25.0", "1.31.2"} {
		if _, stderr, err := render(t, "--kube-version", v); err != nil {
			t.Fatalf("kube %s: %v %s", v, err, stderr)
		}
	}
}

func TestLintIsClean(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not on PATH")
	}
	dir, _ := filepath.Abs(chartDir)
	if out, err := exec.Command(helm, "lint", dir).CombinedOutput(); err != nil {
		t.Fatalf("helm lint: %v\n%s", err, out)
	}
}
