# Model profiles

Tropis needs a reasoning backend. Which one fits depends on your hardware
and on whether cluster data may leave the cluster. Each profile below is a
values file in
[deploy/helm/tropis/examples/](../deploy/helm/tropis/examples/). It sets
only what differs from the chart's defaults, and you install it with `-f`:

```sh
helm install tropis oci://ghcr.io/i-am-webbo/charts/tropis -n tropis-system \
  --create-namespace -f local-gpu-8gb.yaml
```

Edit `baseURL` (and the Secret name for the claude profiles) to match your
cluster. Every setting is described with `backend` in
[values.yaml](../deploy/helm/tropis/values.yaml).

| Profile | Hardware | What leaves the cluster | Speed per analysed node | Dev-corpus behaviour (9-case subset) | Caveats |
|---|---|---|---|---|---|
| [claude](../deploy/helm/tropis/examples/claude.yaml) | None; needs an Anthropic API key Secret | Redacted evidence of each node the pre-filter raises, sent to Anthropic ([SECURITY.md](../SECURITY.md#what-leaves-the-cluster)) | Seconds | Not yet measured | The chart default and the configuration the published accuracy number will be measured with |
| [claude-sweep-escalate](../deploy/helm/tropis/examples/claude-sweep-escalate.yaml) | As claude | As claude, sent once or twice per node | Seconds | Not yet measured | Cheaper, less accurate: a sweep that is confidently wrong is never escalated |
| [local-gpu-8gb](../deploy/helm/tropis/examples/local-gpu-8gb.yaml) | GPU with ~8 GB VRAM running Ollama; `qwen3.5:9b` (6.6 GB) | Nothing | ~2 min (RTX 3070) | 3/5 detected end to end; 0/2 false causal claims; 0 errors; `insufficient_evidence` 1/2 correct | Missed a Kubernetes-layer fault and a read-only remount, calling both coincidental |
| [local-cpu](../deploy/helm/tropis/examples/local-cpu.yaml) | CPU only, Ollama; CPU_MODEL_HERE | Nothing | CPU_SPEED_HERE | CPU_RESULT_HERE | Slow and less accurate than the GPU profile. CPU_CAVEAT_HERE |

## What the numbers are, and are not

**The dev corpus is synthetic.** These numbers were measured on fixtures
generated from `eval/scenarios`, not captured from real hardware. They only
compare the profiles with each other on the same cases, and must not be
quoted as Tropis's accuracy. The published number comes from the capture
run on real hardware ([docs/capture-rig.md](capture-rig.md)); until then
there is none.

Nine cases is also too few for small differences to mean anything. Read
the table as "roughly works" or "does not", not as a ranking.

The subset is nine `npd-absent` fixtures from
[eval/testdata](../eval/testdata/):

| Case | Truth | local-gpu-8gb | local-cpu |
|---|---|---|---|
| degradation-fsync | causal, host | causal ✓ | CPU |
| read-errors-postgres | causal, host | causal ✓ | CPU |
| pending-sectors-slow-probes | causal, host | causal ✓ | CPU |
| write-errors-readonly-remount | causal, host | coincidental ✗ | CPU |
| exhaustion-log-spam | causal, kubernetes | coincidental ✗ | CPU |
| stable-defects-oom | coincidental | coincidental ✓ | CPU |
| read-errors-unused-region-config-crash | coincidental | coincidental ✓ | CPU |
| pending-no-symptoms | insufficient_evidence | insufficient_evidence ✓ | CPU |
| unreadable-smart-restarts | insufficient_evidence | coincidental ✗ | CPU |

Measured 2026-10-12 to 2026-10-17 at prompt v3 with `--concurrency 1
--seed 1`, on an RTX 3070 (8 GB VRAM), 31 GB RAM and a 16-thread CPU.
Ollama 0.40.3 for the GPU run and 0.40.6 for the CPU run. Thinking was
left at each model's default. The CPU run used a second Ollama server with
the GPU hidden (`CUDA_VISIBLE_DEVICES=-1`), so no layer was offloaded to it.

To reproduce, copy those fixture directories into a scratch directory and
run, for example:

```sh
tropis eval --corpus <dir> --backend local --base-url http://localhost:11434 \
  --model qwen3.5:9b --concurrency 1 --seed 1
```

A slow CPU model may need a longer per-case `--timeout` than the default
five minutes.

## Choosing

- **Data may leave and accuracy matters most:** claude.
- **Data may leave, and model cost on every candidate node matters more
  than some accuracy:** claude-sweep-escalate. Measure it against claude
  on your own nodes before relying on it.
- **Nothing may leave and you have a GPU of about 8 GB:** local-gpu-8gb.
  A larger GPU can run a larger model; set `backend.model` and check that
  `backend.contextTokens` still fits.
- **Nothing may leave and there is no GPU:** local-cpu, knowing that it is
  slow and wrong more often. Only nodes the pre-filter raises are analysed,
  so on a healthy cluster it is rarely called, but a sweep that raises
  several nodes takes several times longer than one call. Set `sweep.timeout`
  to cover them.

Do not set `backend.localThink: "false"` to speed a local model up. On the
dev corpus it made the model much faster but overconfident, and it made
false causal claims it did not make with thinking on.
