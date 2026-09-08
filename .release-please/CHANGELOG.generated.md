# Changelog

## 0.1.0 (2026-09-08)


### Features

* **api:** accept a trace sink on OpenOptions and record the session ([#41](https://github.com/P4suta/go-mutants/issues/41)) ([f85242d](https://github.com/P4suta/go-mutants/commit/f85242ddfef1b013dddd9a346d1656c85520f7e9))
* **api:** allow workspace commands after a successful preparation ([#46](https://github.com/P4suta/go-mutants/issues/46)) ([66b7dd0](https://github.com/P4suta/go-mutants/commit/66b7dd0f9472d3df2726d06f91f36ad40a29094c))
* **api:** compile test binaries from the frozen manifest through the overlay ([#59](https://github.com/P4suta/go-mutants/issues/59)) ([6f7b215](https://github.com/P4suta/go-mutants/commit/6f7b215fd690ed4f759b5a49264fc9bc7942fdde))
* **api:** inspect the frozen module before or after preparation ([#60](https://github.com/P4suta/go-mutants/issues/60)) ([283079b](https://github.com/P4suta/go-mutants/commit/283079b14a56eafe39c556b59c707520db8fa47f))
* **api:** name everything a preparation decided, and prove a probe set ([#34](https://github.com/P4suta/go-mutants/issues/34)) ([b5ee1d5](https://github.com/P4suta/go-mutants/commit/b5ee1d519947e94a3877d17d20e32acfe24b526c))
* **api:** name the engine build a consumer runs ([#38](https://github.com/P4suta/go-mutants/issues/38)) ([16eab94](https://github.com/P4suta/go-mutants/commit/16eab948b2b8d07c0d9feb65a7c9a64f7594f810))
* **api:** record the test action log of a target ([#48](https://github.com/P4suta/go-mutants/issues/48)) ([4c761bc](https://github.com/P4suta/go-mutants/commit/4c761bcc15968b38e2be823b0f968a8e5ebc01d0))
* **api:** report output truncation and honour a per-request output limit ([#37](https://github.com/P4suta/go-mutants/issues/37)) ([e76c0ae](https://github.com/P4suta/go-mutants/commit/e76c0aedbd4d981779dbe5b0e2292b916743edee))
* **api:** report the tests that cover each mutant ([#72](https://github.com/P4suta/go-mutants/issues/72)) ([277d75b](https://github.com/P4suta/go-mutants/commit/277d75b7a8fa7951b7b278e65f3c8a01704f1d5f))
* **api:** run a target against the probe tree with Session.Probe ([#15](https://github.com/P4suta/go-mutants/issues/15)) ([fb36fec](https://github.com/P4suta/go-mutants/commit/fb36fecf91a72703925a5e70923ad155f7b28fc8))
* **api:** run the original program through the prepared binaries ([#45](https://github.com/P4suta/go-mutants/issues/45)) ([791d28d](https://github.com/P4suta/go-mutants/commit/791d28ddb7b581d032d70accb2caf93046679d3e))
* **api:** run workspace commands outside preparation's instrumentation window ([#57](https://github.com/P4suta/go-mutants/issues/57)) ([f55bffd](https://github.com/P4suta/go-mutants/commit/f55bffd8ee9e5903d513b3e862debfbc247a358b))
* **api:** select mutants by line range after discovery ([#51](https://github.com/P4suta/go-mutants/issues/51)) ([c999760](https://github.com/P4suta/go-mutants/commit/c999760693cad1e2f3d3c737509ca57aa87cf813))
* **api:** type every failure a consumer has to tell apart ([#31](https://github.com/P4suta/go-mutants/issues/31)) ([db6ff6b](https://github.com/P4suta/go-mutants/commit/db6ff6be00c1f1205bf50a7186bd9c3bd075a21d))
* append an infection log from a probe runtime ([#13](https://github.com/P4suta/go-mutants/issues/13)) ([feae234](https://github.com/P4suta/go-mutants/commit/feae234538f739a6d03195048ab7ed1dd40889b5))
* automate releases with release-please and an approval-gated publish ([#6](https://github.com/P4suta/go-mutants/issues/6)) ([50028b8](https://github.com/P4suta/go-mutants/commit/50028b8aacadb596dc54ae29d59bab59742175ad))
* **cli,engine:** keep temporaries on request and bundle a failed run ([#40](https://github.com/P4suta/go-mutants/issues/40)) ([689cb92](https://github.com/P4suta/go-mutants/commit/689cb92ed667d2a090f10b996405aad2a32f6c19))
* **cli:** add --trace and GO_MUTANTS_TRACE with an owned trace directory ([#36](https://github.com/P4suta/go-mutants/issues/36)) ([d79159c](https://github.com/P4suta/go-mutants/commit/d79159c90bf07a6a6b8b61a5227c39fedf0ce6b3))
* **cli:** add -v and -vv to run, rendered from the same event stream ([#43](https://github.com/P4suta/go-mutants/issues/43)) ([81aac0f](https://github.com/P4suta/go-mutants/commit/81aac0fc27defcd5114a499472815f03ac31148b))
* **cli:** add explain for one mutant or one source position ([#47](https://github.com/P4suta/go-mutants/issues/47)) ([ddd95fb](https://github.com/P4suta/go-mutants/commit/ddd95fbfb347c39f4b766057686868bab6df57f5))
* **cli:** render every retained output and the failing command ([#25](https://github.com/P4suta/go-mutants/issues/25)) ([88f9ca3](https://github.com/P4suta/go-mutants/commit/88f9ca35c25d7b9efa8c4d2ea75c9e3b4edb296e))
* **coverage:** map mutants to the tests that cover them ([#69](https://github.com/P4suta/go-mutants/issues/69)) ([5acc101](https://github.com/P4suta/go-mutants/commit/5acc1014a43e43e453c37608e92e7589eac604d1))
* **discover:** record the coordinates of every suppressed site ([#39](https://github.com/P4suta/go-mutants/issues/39)) ([91bf479](https://github.com/P4suta/go-mutants/commit/91bf479b9bfa5d3de845c102bbda995507aa0723))
* **engine:** let a run name the parent of its temporary directories ([#28](https://github.com/P4suta/go-mutants/issues/28)) ([a1afe95](https://github.com/P4suta/go-mutants/commit/a1afe9560472b8492905a0054440bcc35c6bda1a))
* **engine:** record phases, stages and decisions into one trace ([#33](https://github.com/P4suta/go-mutants/issues/33)) ([1f8b3bf](https://github.com/P4suta/go-mutants/commit/1f8b3bf57a247be31f521eacd3c5e467b77910ff))
* **engine:** size the derived timeout on a baseline run that did not compile ([#68](https://github.com/P4suta/go-mutants/issues/68)) ([6a6fa58](https://github.com/P4suta/go-mutants/commit/6a6fa58a7dc3668ea0f5d2a25c89498e48392907))
* **execute,validate:** record builds, attempts and bisection steps ([#29](https://github.com/P4suta/go-mutants/issues/29)) ([ab39ce2](https://github.com/P4suta/go-mutants/commit/ab39ce2c298760077a2c1edd5e39394619370aa1))
* **execute:** bound each mutant's memory the way its time is bounded ([#61](https://github.com/P4suta/go-mutants/issues/61)) ([c50e19d](https://github.com/P4suta/go-mutants/commit/c50e19da0db1cd8eef44d1d5a6e96f44a12d0e68))
* **execute:** run a mutant against named tests of a binary ([#70](https://github.com/P4suta/go-mutants/issues/70)) ([deb9504](https://github.com/P4suta/go-mutants/commit/deb9504c4bd428ce24345ae32f540e40d20aaae9))
* expose reusable mutation engine API ([#9](https://github.com/P4suta/go-mutants/issues/9)) ([6f26072](https://github.com/P4suta/go-mutants/commit/6f260723e435e643b1ba8cf3175b8e53cd63d637))
* narrow each mutant to the tests that cover it ([#71](https://github.com/P4suta/go-mutants/issues/71)) ([570de00](https://github.com/P4suta/go-mutants/commit/570de00a122b0fa3c4ebf7eaac142b8877fe39fa))
* probe the return-value mutants in the probe tree ([#14](https://github.com/P4suta/go-mutants/issues/14)) ([73e383a](https://github.com/P4suta/go-mutants/commit/73e383a34b77abcc16c6a5a05ce9b61dc419b83a))
* report the branch a decreasing edit gates ([#12](https://github.com/P4suta/go-mutants/issues/12)) ([d0cdb7e](https://github.com/P4suta/go-mutants/commit/d0cdb7ed194e1a018d63711ba918d672669d73c5))
* **report:** add timing, executions, validation and toolchain facts ([#42](https://github.com/P4suta/go-mutants/issues/42)) ([36924b8](https://github.com/P4suta/go-mutants/commit/36924b8934369021cf4b689b9c0308da805ede68))
* **runner:** record every subprocess and name the failing command ([#23](https://github.com/P4suta/go-mutants/issues/23)) ([a08c104](https://github.com/P4suta/go-mutants/commit/a08c104e8c4f8e3e6789f487967729996edd62b9))
* **trace:** add the gomutants-trace-v1 recorder, sinks and schema ([#21](https://github.com/P4suta/go-mutants/issues/21)) ([d82e920](https://github.com/P4suta/go-mutants/commit/d82e920dc4d3ee8972785457cecf7c2151b2b07f))
* **workspace:** own every temporary directory, sweep orphans, keep on request ([#16](https://github.com/P4suta/go-mutants/issues/16)) ([b5d08c8](https://github.com/P4suta/go-mutants/commit/b5d08c832c89abe0724d01e566ed500424824fc2))


### Bug Fixes

* **runner:** measure a child's memory with the sampler alone on Linux ([#64](https://github.com/P4suta/go-mutants/issues/64)) ([6f6d753](https://github.com/P4suta/go-mutants/commit/6f6d7535a70ade6fc5393292f851a6571065c099))
* **testkit:** isolate helper coverage output for a binary run with -test.gocoverdir ([#63](https://github.com/P4suta/go-mutants/issues/63)) ([20554a6](https://github.com/P4suta/go-mutants/commit/20554a6bbde6b299c6507cac4aaf454474b4d3b7))
* **testkit:** stop leaking a coverage directory per helper child ([#55](https://github.com/P4suta/go-mutants/issues/55)) ([0a9fe9c](https://github.com/P4suta/go-mutants/commit/0a9fe9cc3eb6545112fa23220d1189650c69e108))
* **testkit:** stop removing the kept package directory instead of locking it ([#50](https://github.com/P4suta/go-mutants/issues/50)) ([bca213b](https://github.com/P4suta/go-mutants/commit/bca213bad03f14896639b66a7a84100b40e91d5d))
* **test:** stop asking git to turn signing off in two suites, and gate it ([#66](https://github.com/P4suta/go-mutants/issues/66)) ([7d1267c](https://github.com/P4suta/go-mutants/commit/7d1267c8306709cb17f39049ffa732d60c3df330))


### Performance Improvements

* **api:** run workspace controls concurrently ([#18](https://github.com/P4suta/go-mutants/issues/18)) ([c0533e7](https://github.com/P4suta/go-mutants/commit/c0533e78ca6c88f5d46e4b23c8d79ab223c76120))
* **api:** scope discovery, overlap preparation, and parallelize the snapshot copy ([#19](https://github.com/P4suta/go-mutants/issues/19)) ([02e88ba](https://github.com/P4suta/go-mutants/commit/02e88bae7dd5ee992aaf722dc75c79c0e20b2aa5))
* **snapshot:** copy the tree's modification times with it ([#20](https://github.com/P4suta/go-mutants/issues/20)) ([6e15244](https://github.com/P4suta/go-mutants/commit/6e152442b74acb7df5dd1a6d16489f479b311350))
* **snapshot:** one snapshot directory name per source root ([#17](https://github.com/P4suta/go-mutants/issues/17)) ([64b2dbc](https://github.com/P4suta/go-mutants/commit/64b2dbc5a1b7e4dd2f776a6b8837a2694e277043))
