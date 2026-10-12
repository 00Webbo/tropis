# Changelog

## [0.4.0](https://github.com/00Webbo/tropis/compare/v0.3.2...v0.4.0) (2026-10-12)


### ⚠ BREAKING CHANGES

* **smart:** PromptVersion is v3 (new prompts/v3.md, one sentence on what a timeout means, and a reshaped host.unreadable). Eval results are not comparable with v2 results.

### Bug fixes

* **capture:** find the scarred drive by serial, not kernel path ([5c99b48](https://github.com/00Webbo/tropis/commit/5c99b48b0399eea0bfcf18a4b866d92e8f11f5b3))
* **chart:** alert on a stalled sweep, not on old reports ([5a71e21](https://github.com/00Webbo/tropis/commit/5a71e218f1084bed758260910b64f3e1fd1d5fee))
* **chart:** dial the collector on the port collector.port sets ([b622f3e](https://github.com/00Webbo/tropis/commit/b622f3e472b9c35b2991b96776247a41cbc67ff2))
* **cli:** make flags and timeouts consistent across commands ([219c73f](https://github.com/00Webbo/tropis/commit/219c73f0b7a95a65c038b47df829bb7543d458c6))
* **collector:** treat a non-positive --history-interval as the default ([5a3da8c](https://github.com/00Webbo/tropis/commit/5a3da8c18778c9cf6c0d56bd0bf01598a4eee6b1))
* **devcorpus:** drop gen-devcorpus's -o flag ([80b66a6](https://github.com/00Webbo/tropis/commit/80b66a6cb8cdba1c5bc83a47feb972e96418827a))
* **npd-plugin:** call the SMART check once per interval ([bb8492d](https://github.com/00Webbo/tropis/commit/bb8492dc29cbc505128ea827504e228e90b0c5a5))
* **reason:** reject trailing brackets and text after a code fence ([82428d4](https://github.com/00Webbo/tropis/commit/82428d4625777d570dadcbffd45f31a055a056e1))
* **smart:** report a drive that hangs on smartctl instead of dropping it ([79b6881](https://github.com/00Webbo/tropis/commit/79b6881492ae9f67029368c3a9cfc0c3b0af7d4c))


### Performance

* **k8s:** ask only for the node's events, and label new reports at create ([585eeb1](https://github.com/00Webbo/tropis/commit/585eeb181721c76c9dc88e35fe824bfcdc208f1b))


### Dependencies

* update Go toolchain and base images ([ee1a599](https://github.com/00Webbo/tropis/commit/ee1a599c4d7b31ef221bc325d901b7ff8cc08c28))

## [0.3.2](https://github.com/00Webbo/tropis/compare/v0.3.1...v0.3.2) (2026-10-11)


### Bug fixes

* **capture:** refuse a stable-defect fixture without stable defects ([a04e2b3](https://github.com/00Webbo/tropis/commit/a04e2b30153315ead9009fb2d22c162a9af5e2b8))

## [0.3.1](https://github.com/00Webbo/tropis/compare/v0.3.0...v0.3.1) (2026-10-11)


### Bug fixes

* **capture:** refuse a fixture whose SMART did not change ([8c8d5b9](https://github.com/00Webbo/tropis/commit/8c8d5b949443cb146476ddca670a4a3e4b652bc6))
* **prefilter:** raise on a container's ephemeral-storage limit eviction ([620b4f6](https://github.com/00Webbo/tropis/commit/620b4f679ccc07b317fa8544feecf87b3c7de734))
* **scenarios:** make disk exhaustion reproducible on the rig ([0473b75](https://github.com/00Webbo/tropis/commit/0473b75a9e12d32c888fa28a57baa79a5010b1f8))

## [0.3.0](https://github.com/00Webbo/tropis/compare/v0.2.0...v0.3.0) (2026-10-11)


### Features

* **eval:** add a per-case --timeout flag ([6c34b68](https://github.com/00Webbo/tropis/commit/6c34b689b724f6ab62f30426971bcaf9a636fcf6))
* **local:** control model thinking ([b1a4f53](https://github.com/00Webbo/tropis/commit/b1a4f53926bb3bd6b9cc3a6fd2e14489354264e8))


### Bug fixes

* **local:** reject a think setting on the openai protocol ([ffc1666](https://github.com/00Webbo/tropis/commit/ffc16662b7661f5f7c0033bc8e8c4e215b94da0d))

## [0.2.0](https://github.com/00Webbo/tropis/compare/v0.1.1...v0.2.0) (2026-10-07)


### ⚠ BREAKING CHANGES

* **eval:** the gate now applies to end-to-end detection instead of root-cause accuracy, and runner.GateMinRootCauseAccuracy is replaced by runner.GateMinEndToEndDetection.
* **reason:** PromptVersion is v2; eval results are not comparable with v1 results.

### Features

* **eval:** gate on end-to-end detection ([f023f1f](https://github.com/00Webbo/tropis/commit/f023f1f7a64f49a40abd7a5771ac263cef533a48))
* **prefilter:** raise nodes on Kubernetes storage symptoms ([0bdf6c2](https://github.com/00Webbo/tropis/commit/0bdf6c2de1d262246f6df12b3bf18a53a10e4fdf))


### Bug fixes

* **reason:** keep trigger names out of model input ([3cfd7ca](https://github.com/00Webbo/tropis/commit/3cfd7ca1524979eddc7c5cc9df0f854fbd551144))

## [0.1.1](https://github.com/00Webbo/tropis/compare/v0.1.0...v0.1.1) (2026-10-05)


### Bug fixes

* **local:** stop Ollama silently truncating the model input ([bf241bb](https://github.com/00Webbo/tropis/commit/bf241bb2b442b256f118e3d500fb06ed3195d6dc))

## 0.1.0 (2026-10-05)


### Features

* Tropis v1 diagnostic agent, evaluation harness and packaging ([79c6397](https://github.com/00Webbo/tropis/commit/79c6397b8ea51bfecb0b84721cd22fba4fdc1918))
