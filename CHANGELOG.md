# Changelog

## [0.1.4](https://github.com/muster-io/muster/compare/v0.1.3...v0.1.4) (2026-10-26)


### Bug Fixes

* **config:** keep the database URL query intact when adding the default sslmode ([#394](https://github.com/muster-io/muster/issues/394)) ([9ea91a7](https://github.com/muster-io/muster/commit/9ea91a7bf19a08162f6566644c66a23fee207bd1))
* **db:** drop pool settings from the session connection derived from MUSTER_DATABASE_URL ([#392](https://github.com/muster-io/muster/issues/392)) ([dfcf563](https://github.com/muster-io/muster/commit/dfcf563b5060473d316fa9abe82a0d4108e1c119))

## [0.1.3](https://github.com/muster-io/muster/compare/v0.1.2...v0.1.3) (2026-10-26)


### Features

* **mattermost:** leave the summary line out of root messages and keep only the attachment ([#366](https://github.com/muster-io/muster/issues/366)) ([0abf6f3](https://github.com/muster-io/muster/commit/0abf6f397d3a5264400e25271980627bdd703128))
* **messages:** write the Owner and other names in the footer as mentions when the account is linked ([#387](https://github.com/muster-io/muster/issues/387)) ([7210552](https://github.com/muster-io/muster/commit/72105526c4bc8b976706063622fa4389fcba6ef0))


### Bug Fixes

* **mattermost:** name the bot and the roles read when the check warns about ephemeral answers ([#386](https://github.com/muster-io/muster/issues/386)) ([03e0984](https://github.com/muster-io/muster/commit/03e09842e02304731611b90b827b91be87a373a0))


### Dependencies

* **deps:** update tanstack ([#383](https://github.com/muster-io/muster/issues/383)) ([e5ba78b](https://github.com/muster-io/muster/commit/e5ba78beffbfe20a0218fa5b0adebaf94a673e9f))
* **deps:** update zod to 4.6.6 ([#377](https://github.com/muster-io/muster/issues/377)) ([1391ff2](https://github.com/muster-io/muster/commit/1391ff2279a3c46885b5550beece5fb599a73172))

## [0.1.2](https://github.com/muster-io/muster/compare/v0.1.1...v0.1.2) (2026-10-22)


### Bug Fixes

* **oidc:** request offline access only from providers that offer it ([#361](https://github.com/muster-io/muster/issues/361)) ([ac1af33](https://github.com/muster-io/muster/commit/ac1af33ef3152467d276072ce9d54a154d75c0ea))

## [0.1.1](https://github.com/muster-io/muster/compare/v0.1.0...v0.1.1) (2026-10-22)


### Features

* **helm:** render extra objects such as an external secret with the release ([#357](https://github.com/muster-io/muster/issues/357)) ([e58d6ab](https://github.com/muster-io/muster/commit/e58d6ab39d38b56e19773ece3fd25ee8cdcf1665))


### Bug Fixes

* **oidc:** keep the groups claim seen at sign-in when the check finds it not advertised ([#354](https://github.com/muster-io/muster/issues/354)) ([40e83c2](https://github.com/muster-io/muster/commit/40e83c257c634e085ce942b7837b03d07e84ab83))

## 0.1.0 (2026-10-21)


### Features

* **accountlinks:** link messenger accounts from the profile and attribute presses ([#202](https://github.com/muster-io/muster/issues/202)) ([4664e48](https://github.com/muster-io/muster/commit/4664e488062a1471a04d09449e5d856e0a5d68ef))
* add release pipeline, container image, Helm chart and compose example ([aa46120](https://github.com/muster-io/muster/commit/aa46120074ebe22e304b41424cc2e61c69a7aa21))
* **api:** add codes to connection and destination check results ([#243](https://github.com/muster-io/muster/issues/243)) ([c276541](https://github.com/muster-io/muster/commit/c276541b0b7f9d60013659801f41fca4497e4d72))
* **api:** add reason codes, link code attempts and heartbeat error classes ([#242](https://github.com/muster-io/muster/issues/242)) ([6c58aa8](https://github.com/muster-io/muster/commit/6c58aa896b29b41a73afe76f12e4d58a42147b15))
* **auth:** add API server with local sign-in, sessions and the bootstrap admin ([#108](https://github.com/muster-io/muster/issues/108)) ([de3e476](https://github.com/muster-io/muster/commit/de3e4764d33d54d1ae9367b418f5d26e17a164f0))
* **auth:** add oidc account linking, conversion to local and background re-checks ([#117](https://github.com/muster-io/muster/issues/117)) ([f5814cc](https://github.com/muster-io/muster/commit/f5814ccd8b45515a9f90fa655021128e9fa58c5b))
* **auth:** add oidc settings, proxy settings and oidc sign-in ([#113](https://github.com/muster-io/muster/issues/113)) ([1306b61](https://github.com/muster-io/muster/commit/1306b61e2ff6eb14f10e6e2177fffe3b130ccfe5))
* **auth:** add totp, the totp policy, system notices and live updates ([#112](https://github.com/muster-io/muster/issues/112)) ([56b87be](https://github.com/muster-io/muster/commit/56b87be8f47afb2dda69a1063afe774284854d15))
* **bridge:** add bridge connections and the bridge protocol client ([#249](https://github.com/muster-io/muster/issues/249)) ([7d381cc](https://github.com/muster-io/muster/commit/7d381cce304f5127372ea9c9891e04af31c6ca9c))
* **bridge:** add bridge destinations and their channel check ([#250](https://github.com/muster-io/muster/issues/250)) ([eb8f89a](https://github.com/muster-io/muster/commit/eb8f89a048b70f184ced2596cef76dbe8cbd32dd))
* **bridge:** deliver alert groups through bridges with idempotent posts ([#251](https://github.com/muster-io/muster/issues/251)) ([716353f](https://github.com/muster-io/muster/commit/716353f8f71bacfa7d025d1681147bbe60bf83ff))
* **bridge:** link messenger accounts through bridges and mention linked people ([#255](https://github.com/muster-io/muster/issues/255)) ([83f2878](https://github.com/muster-io/muster/commit/83f28788b3919ee49858e853f85be2b01c384fbc))
* **bridge:** receive bridge events by polling and push and run text commands ([#253](https://github.com/muster-io/muster/issues/253)) ([fd440f8](https://github.com/muster-io/muster/commit/fd440f8c4a03d3348465095b09dd04bb509becc0))
* **bridge:** republish bridge root messages past the messenger's edit window ([#252](https://github.com/muster-io/muster/issues/252)) ([2f957fa](https://github.com/muster-io/muster/commit/2f957fa71a77caac35e3268b10c407d484b3b484))
* **chart:** add alert rules with unit tests and the grafana dashboard ([#226](https://github.com/muster-io/muster/issues/226)) ([3ccb43b](https://github.com/muster-io/muster/commit/3ccb43bd17f6e41120a9ac082f3ec7ba346c97b1))
* **cli:** add the bridge conformance check ([#262](https://github.com/muster-io/muster/issues/262)) ([91da52e](https://github.com/muster-io/muster/commit/91da52ea7e4613dd75992dc86a9ce2cebc55842c))
* **commands:** snooze until the next working morning or the start of the week ([#300](https://github.com/muster-io/muster/issues/300)) ([e014223](https://github.com/muster-io/muster/commit/e0142239d8dee90be935ec8fd2a10c136766110a))
* **connections:** check a connection before saving it ([#271](https://github.com/muster-io/muster/issues/271)) ([09c84ae](https://github.com/muster-io/muster/commit/09c84aed35c04db4f690866b889795241bf35d57))
* **connections:** check a slack connection before saving it ([#329](https://github.com/muster-io/muster/issues/329)) ([e45aa49](https://github.com/muster-io/muster/commit/e45aa493d6a3069d693c97d1f01dc6473fcdc7e6))
* **delivery:** add error classes, broken destinations and recovery, storms and republication ([#149](https://github.com/muster-io/muster/issues/149)) ([c711902](https://github.com/muster-io/muster/commit/c7119022a01835d8282fd08eca563482939e9b79))
* **delivery:** offer the snooze presets on messenger buttons and in text commands ([#301](https://github.com/muster-io/muster/issues/301)) ([93b1306](https://github.com/muster-io/muster/commit/93b1306980312db1e493b9dd6042b0f706bf0ac4))
* **delivery:** reconcile root messages with limiters, an interactive path and batched threads ([#148](https://github.com/muster-io/muster/issues/148)) ([36ab2df](https://github.com/muster-io/muster/commit/36ab2df24f08794e51abc6d4d3333aec32f02183))
* **delivery:** send the alert group hint when a delivery ends ([#172](https://github.com/muster-io/muster/issues/172)) ([2a6cc06](https://github.com/muster-io/muster/commit/2a6cc0698d71aa1011f5bb611d30aa1d24e9797f))
* **delivery:** style the acknowledge, resolve and still-on-it buttons ([#245](https://github.com/muster-io/muster/issues/245)) ([1bf5d20](https://github.com/muster-io/muster/commit/1bf5d2086fba6c6524a003b4d8d221f1366119cc))
* **destinations:** add destination tests and previews for every type ([#190](https://github.com/muster-io/muster/issues/190)) ([a94a116](https://github.com/muster-io/muster/commit/a94a11628ae21dccaae3d79b6ba1355cdf4a4bd3))
* **groups:** add commands, takeover, refusals and bulk commands ([#145](https://github.com/muster-io/muster/issues/145)) ([cc1b2b2](https://github.com/muster-io/muster/commit/cc1b2b2e19e3898a5e1761308e6c2f9795feae99))
* **groups:** add notes, snooze ends, owner filters and the release of disabled owners ([#146](https://github.com/muster-io/muster/issues/146)) ([9cb3a6d](https://github.com/muster-io/muster/commit/9cb3a6d26e8192f6397c1866cc6f1ffbc6c49b66))
* **groups:** add the alert group lifecycle, the timeline and lifecycle events ([#140](https://github.com/muster-io/muster/issues/140)) ([71d88a3](https://github.com/muster-io/muster/commit/71d88a3d2059055ac79a91a162852a21b64002b6))
* **groups:** add the alert group list, search, counts, statistics, retention and live hints ([#142](https://github.com/muster-io/muster/issues/142)) ([89856ef](https://github.com/muster-io/muster/commit/89856ef4b4ceecbd0559a35024765e85ad8db14e))
* **heartbeat:** add the heartbeat endpoint, leader checks and the stale scan ([#130](https://github.com/muster-io/muster/issues/130)) ([dcdd7d7](https://github.com/muster-io/muster/commit/dcdd7d7e48dba3840f9cfc962d3d01a910780b9a))
* **helm:** read the database password or url from a secret of its own ([#321](https://github.com/muster-io/muster/issues/321)) ([aa11fa4](https://github.com/muster-io/muster/commit/aa11fa451ca8a28cfbb7540cecc2361fb769cff8))
* **ingest:** add integrations, integration tokens and the ingestion endpoint ([#122](https://github.com/muster-io/muster/issues/122)) ([52588ee](https://github.com/muster-io/muster/commit/52588ee3ac3b038720da75c4a0f75effd8478014))
* **ingest:** add internal alerts, the built-in integration, replay and learned routes ([#128](https://github.com/muster-io/muster/issues/128)) ([7825756](https://github.com/muster-io/muster/commit/78257561e3b518cf418d603327175cc204f3692d))
* **ingest:** process snapshots into alerts with gone, truncation and continuation semantics ([#126](https://github.com/muster-io/muster/issues/126)) ([f141a36](https://github.com/muster-io/muster/commit/f141a3671c5499ee9264a17a7301b88603eccfd8))
* **ingest:** take Snapshots from Grafana Alerting's webhook contact point ([#305](https://github.com/muster-io/muster/issues/305)) ([6840a8d](https://github.com/muster-io/muster/commit/6840a8dd6692da757e81424ee2fe9a84b3fce933))
* **keyring:** add master key keyring, key canary and organization defaults ([#105](https://github.com/muster-io/muster/issues/105)) ([680496a](https://github.com/muster-io/muster/commit/680496a5a524a24e61c7ac51c670ef23a24a4774))
* **keyring:** show the keyring status and rotate the master key ([#217](https://github.com/muster-io/muster/issues/217)) ([af417db](https://github.com/muster-io/muster/commit/af417db825dfe2be73f264e0c4161b5a84397a2a))
* **leader:** prune ended sessions and stale sign-in throttles ([#109](https://github.com/muster-io/muster/issues/109)) ([02ca570](https://github.com/muster-io/muster/commit/02ca570696ef6af264ba856d41c4bee66bdaf55f))
* **links:** add link kinds and name the rules of an in_use table ([#158](https://github.com/muster-io/muster/issues/158)) ([a479f55](https://github.com/muster-io/muster/commit/a479f559da1e05a6b2fd7416da86055edc92c776))
* **links:** add lookup tables, link rules and mention settings ([#154](https://github.com/muster-io/muster/issues/154)) ([4ab9609](https://github.com/muster-io/muster/commit/4ab9609b0a3f49862fa125e327fe8073e06c0a17))
* **logging:** add domain logger with log event and metric registries ([#99](https://github.com/muster-io/muster/issues/99)) ([3f1d72f](https://github.com/muster-io/muster/commit/3f1d72fbdaa41f9fb9df45cab34ad36eb54a4988))
* **mattermost:** add mattermost connections, destinations and the destination check ([#159](https://github.com/muster-io/muster/issues/159)) ([94c5023](https://github.com/muster-io/muster/commit/94c50234e650a4e04309d60dd5d03ac4aa312e32))
* **mattermost:** add the mattermost adapter, button callbacks and the delivery problem filter ([#161](https://github.com/muster-io/muster/issues/161)) ([7818264](https://github.com/muster-io/muster/commit/78182643e636fe71ea48aac48cea0ed2aba92b06))
* **messages:** add the template sandbox, the default message, built-in texts and previews ([#152](https://github.com/muster-io/muster/issues/152)) ([7923d45](https://github.com/muster-io/muster/commit/7923d45d3f993bafb4fea301e7daa0af4a7f389f))
* **observability:** add the outgoing heartbeat, system status and the oidc secret expiry alert ([#204](https://github.com/muster-io/muster/issues/204)) ([7077820](https://github.com/muster-io/muster/commit/7077820329144b649a6202ee1e7a46453d828d63))
* **organization:** edit organization settings and the outbound policy ([#214](https://github.com/muster-io/muster/issues/214)) ([0897e4e](https://github.com/muster-io/muster/commit/0897e4e7a958eb915692998f21f0041a7a327c74))
* **outbound:** add outbound HTTP package with address policy and proxies ([#107](https://github.com/muster-io/muster/issues/107)) ([61063d4](https://github.com/muster-io/muster/commit/61063d475180f71c496f3a3c105ec586487ad469))
* **outbound:** identify muster in the user agent of outbound requests ([#264](https://github.com/muster-io/muster/issues/264)) ([3a5100e](https://github.com/muster-io/muster/commit/3a5100ed8db29b44e4fbaaedeb6f2eb9f4cfce9e))
* **requestlog:** record callback requests and the runs of checks ([#273](https://github.com/muster-io/muster/issues/273)) ([4869a9f](https://github.com/muster-io/muster/commit/4869a9f8c474d2413676df6bccd4687ea9e9833a))
* **requestlog:** record outbound requests with their answers ([#272](https://github.com/muster-io/muster/issues/272)) ([9c85246](https://github.com/muster-io/muster/commit/9c852462b78f278807d357ddfc5c6ff788453368))
* **routing:** add routes, matchers, evaluation order, severity levels and route profiles ([#133](https://github.com/muster-io/muster/issues/133)) ([150f489](https://github.com/muster-io/muster/commit/150f489aae74275a863d2f5a65eebb1d2ba5b67a))
* **routing:** add the group key preview and route suggestions ([#134](https://github.com/muster-io/muster/issues/134)) ([5c3d7bc](https://github.com/muster-io/muster/commit/5c3d7bc70c3e63fec9f7fef057c8f914b74fe12a))
* **runtime:** add bootstrap settings, database connections, migrations and listeners ([#102](https://github.com/muster-io/muster/issues/102)) ([93ab7f5](https://github.com/muster-io/muster/commit/93ab7f59b24bb913c0b8a8f0567b6c1d11156e0a))
* **runtime:** add leader election, partition maintenance, downtime recovery and doctor ([#106](https://github.com/muster-io/muster/issues/106)) ([b04c8cb](https://github.com/muster-io/muster/commit/b04c8cbb47d1bd3f1d10bafac9a5ffcc1f336c3f))
* **slack:** add slack connections and the slack web api client ([#323](https://github.com/muster-io/muster/issues/323)) ([f61bdf4](https://github.com/muster-io/muster/commit/f61bdf4aedef53007f88f543c07d6e5b34194e42))
* **slack:** add slack destinations and their channel check ([#324](https://github.com/muster-io/muster/issues/324)) ([10b212f](https://github.com/muster-io/muster/commit/10b212f2080de2b7c36d22f0917e07d8a6353cad))
* **slack:** deliver alert groups to slack channels ([#325](https://github.com/muster-io/muster/issues/325)) ([b42f5c0](https://github.com/muster-io/muster/commit/b42f5c03c9a42b79ffd8eba99a0a4dce9f094a5a))
* **slack:** link slack accounts by a code and mention linked people ([#328](https://github.com/muster-io/muster/issues/328)) ([90e0f29](https://github.com/muster-io/muster/commit/90e0f29502e2bdb27068aa209741c9570e9acf41))
* **slack:** receive slack button presses over http and answer them ([#326](https://github.com/muster-io/muster/issues/326)) ([0d37ba0](https://github.com/muster-io/muster/commit/0d37ba060052b99e3ff0f665ef114fbe698fb9de))
* **slack:** receive slack button presses over socket mode on the leader ([#327](https://github.com/muster-io/muster/issues/327)) ([a49e2a4](https://github.com/muster-io/muster/commit/a49e2a4f24724baf0de9b05068e16c912055c228))
* **telegram:** add button presses with callback answers and the update router lock ([#183](https://github.com/muster-io/muster/issues/183)) ([6bd1537](https://github.com/muster-io/muster/commit/6bd1537df2696a5dbfe2667f94949803ed8ce641))
* **telegram:** add comment threads with the copy buffer and unattached replies ([#182](https://github.com/muster-io/muster/issues/182)) ([c1f9c06](https://github.com/muster-io/muster/commit/c1f9c06370ceab7f0288484c22864052067c8e98))
* **telegram:** add telegram connections with the dry probe, long polling and webhooks ([#173](https://github.com/muster-io/muster/issues/173)) ([4709c2e](https://github.com/muster-io/muster/commit/4709c2e13f6c7a42e07d8ca2dce24e9612125a98))
* **telegram:** add telegram destinations, the destination check and root message posts ([#181](https://github.com/muster-io/muster/issues/181)) ([60b5263](https://github.com/muster-io/muster/commit/60b5263effd249c5793baaf693467212f48fdaba))
* **timers:** add ack timeouts, unclaimed, reminders and auto-unacknowledge ([#199](https://github.com/muster-io/muster/issues/199)) ([4659f7a](https://github.com/muster-io/muster/commit/4659f7a4ff01e7cb24643371f2fd7b12fb6540b8))
* **timers:** add still on it and unack buttons to reminders in mattermost and telegram ([#201](https://github.com/muster-io/muster/issues/201)) ([dc7c22f](https://github.com/muster-io/muster/commit/dc7c22f488337bcfcf06900e31eada0e6fdcb930))
* **tokens:** add personal access tokens, service accounts and token authentication ([#120](https://github.com/muster-io/muster/issues/120)) ([c224e3c](https://github.com/muster-io/muster/commit/c224e3cb930e66da72744cb135a37ce7bc59e5df))
* **users:** add user administration, password setup links and the audit log api ([#110](https://github.com/muster-io/muster/issues/110)) ([4560e0b](https://github.com/muster-io/muster/commit/4560e0b9c1a511379fafd9724c8fabd22fc25891))
* **web:** add ack timeout and reminder settings, unclaimed and still on it ([#200](https://github.com/muster-io/muster/issues/200)) ([51f4683](https://github.com/muster-io/muster/commit/51f4683890131cc6459223f5153e1dba6ad2d1cb))
* **web:** add application shell, sign-in, password setup, totp and profile pages ([#118](https://github.com/muster-io/muster/issues/118)) ([1bf122e](https://github.com/muster-io/muster/commit/1bf122e0e718d90a703204425b995415839b06e1))
* **web:** add bridge connection and destination pages and linking through bridges ([#261](https://github.com/muster-io/muster/issues/261)) ([88216d0](https://github.com/muster-io/muster/commit/88216d09648f569eb764ce77012a28be0fe385cc))
* **web:** add command buttons, dialogs, the note box and bulk selection ([#147](https://github.com/muster-io/muster/issues/147)) ([941ea42](https://github.com/muster-io/muster/commit/941ea42c42f24e31de783502a04337d36d081c9a))
* **web:** add destination pages, route delivery settings, delivery state and the delivery problem filter ([#170](https://github.com/muster-io/muster/issues/170)) ([a80d05e](https://github.com/muster-io/muster/commit/a80d05e0feac69fedc533bffb3f5482008dacc49))
* **web:** add heartbeat settings, state badges and banners ([#132](https://github.com/muster-io/muster/issues/132)) ([279d3b4](https://github.com/muster-io/muster/commit/279d3b4d3f732b3fc575978acd7c232728b3f052))
* **web:** add integration pages, the token dialog and the stored snapshot viewer ([#123](https://github.com/muster-io/muster/issues/123)) ([da66e77](https://github.com/muster-io/muster/commit/da66e77977014a6a9ff83be3e1785002cc9b25fe))
* **web:** add learned alertmanager routes, warnings and the alerts view ([#129](https://github.com/muster-io/muster/issues/129)) ([878605b](https://github.com/muster-io/muster/commit/878605bf6880e3b76e32700230638920fe6c3f01))
* **web:** add messenger accounts to the profile and account links to the user page ([#203](https://github.com/muster-io/muster/issues/203)) ([ddef7ec](https://github.com/muster-io/muster/commit/ddef7ec4a75a9cc562f03cf1659cdf19e2ed7808))
* **web:** add personal access token and service account pages ([#121](https://github.com/muster-io/muster/issues/121)) ([ab1fb74](https://github.com/muster-io/muster/commit/ab1fb7466b4331739c8887ffe8f86d793fa18442))
* **web:** add route message editors, lookup tables and link rules pages ([#156](https://github.com/muster-io/muster/issues/156)) ([c924817](https://github.com/muster-io/muster/commit/c924817c12fcb4ab5002bd24ef88c76befdb7d3d))
* **web:** add slack connection and destination pages and linking slack accounts ([#331](https://github.com/muster-io/muster/issues/331)) ([4b32468](https://github.com/muster-io/muster/commit/4b324681848d89db853dc54202dfd37c9ba6acd4))
* **web:** add telegram connection pages and the telegram destination form ([#184](https://github.com/muster-io/muster/issues/184)) ([b523893](https://github.com/muster-io/muster/commit/b523893d0a0aca02b35daa898ba84b0fa16e0738))
* **web:** add the alert group list and page, usable at phone width ([#143](https://github.com/muster-io/muster/issues/143)) ([8979f6f](https://github.com/muster-io/muster/commit/8979f6fb0dc314eb34a797f65881886ac2912bfe))
* **web:** add the destination test and preview panels ([#195](https://github.com/muster-io/muster/issues/195)) ([829828b](https://github.com/muster-io/muster/commit/829828b3b9a94107ee516f40e3d2e9c98ed4e5e2))
* **web:** add the mattermost connection pages ([#168](https://github.com/muster-io/muster/issues/168)) ([d303aa8](https://github.com/muster-io/muster/commit/d303aa81c9e598c26cfa379c8893e0973d6c3994))
* **web:** add the organization settings pages under one organization entry ([#218](https://github.com/muster-io/muster/issues/218)) ([53d86ab](https://github.com/muster-io/muster/commit/53d86ab28e29c9614eeac236e59c54ec5953885d))
* **web:** add the outgoing webhook destination form with secrets and signing secret actions ([#188](https://github.com/muster-io/muster/issues/188)) ([c8ae2dd](https://github.com/muster-io/muster/commit/c8ae2dd3bf9240ca1460c13993eff17c531b7731))
* **web:** add the request log tab and the record view ([#274](https://github.com/muster-io/muster/issues/274)) ([ff4b8a5](https://github.com/muster-io/muster/commit/ff4b8a58f4da76279489f5bbc1387ca85eba3c59))
* **web:** add the routes list, route editor, matcher builder and group key preview ([#135](https://github.com/muster-io/muster/issues/135)) ([72093b1](https://github.com/muster-io/muster/commit/72093b10f26c27469367c3979435a39b524b5c80))
* **web:** add the statistics page, route lifecycle settings and delete dialog additions ([#144](https://github.com/muster-io/muster/issues/144)) ([8ea5677](https://github.com/muster-io/muster/commit/8ea56774015416c361af8ad78800ad8de3c3dc56))
* **web:** add the system status page and the outgoing heartbeat settings ([#211](https://github.com/muster-io/muster/issues/211)) ([67f7d8e](https://github.com/muster-io/muster/commit/67f7d8eca7d07d0075a157ad8bab9fefd458dd14))
* **web:** add users, oidc settings, security and audit log pages ([#119](https://github.com/muster-io/muster/issues/119)) ([a989e8d](https://github.com/muster-io/muster/commit/a989e8d33d6a282ed13d8d5ac26548bb861426c5))
* **web:** compact the top navigation with access and organization groups ([#219](https://github.com/muster-io/muster/issues/219)) ([e977882](https://github.com/muster-io/muster/commit/e97788227ebfbdc720410b8e6cb04279042172dd))
* **webhooks:** add the outgoing webhook events mode with signing and secrets ([#175](https://github.com/muster-io/muster/issues/175)) ([da6dd9e](https://github.com/muster-io/muster/commit/da6dd9e54b791f9473f17dff67dbfa8f567e9377))
* **webhooks:** add the outgoing webhook template mode with extraction and threads ([#186](https://github.com/muster-io/muster/issues/186)) ([3b06982](https://github.com/muster-io/muster/commit/3b0698205e216432dcf600a126e5167a7710ed19))
* **web:** label connections and destinations and warn about duplicate chats ([#207](https://github.com/muster-io/muster/issues/207)) ([d8cd846](https://github.com/muster-io/muster/commit/d8cd846b080727fb9976a3f7f15612f5c247abed))
* **web:** link complex fields and problems to their guides ([#351](https://github.com/muster-io/muster/issues/351)) ([f3370b1](https://github.com/muster-io/muster/commit/f3370b1b9f46e174535d81cac487922e355f15a7))
* **web:** link page titles to the documentation of the running version ([#347](https://github.com/muster-io/muster/issues/347)) ([0317f01](https://github.com/muster-io/muster/commit/0317f01256e4991e2472fa61f44f8175592ff482))
* **web:** link the organization, access and profile pages to the documentation ([#353](https://github.com/muster-io/muster/issues/353)) ([33b7647](https://github.com/muster-io/muster/commit/33b764759b449f7e4ad154ae59ef8dc974eaa876))
* **web:** offer the snooze presets in the snooze dialog and set the working week ([#302](https://github.com/muster-io/muster/issues/302)) ([c77f9bb](https://github.com/muster-io/muster/commit/c77f9bb00b44053b622bea271489d31aeb3a5df4))
* **web:** show whether live updates are connected in the header ([#303](https://github.com/muster-io/muster/issues/303)) ([50198a0](https://github.com/muster-io/muster/commit/50198a0a198b192dc8f02a88ec4223884662e543))


### Bug Fixes

* **accountlinks:** lock the user before the link request for token links ([#285](https://github.com/muster-io/muster/issues/285)) ([35c4b5d](https://github.com/muster-io/muster/commit/35c4b5ddb231960aa05017b8a3e2ccf37f4143e8))
* **api:** round learned route intervals once and report matcher regexp errors as typed ([#131](https://github.com/muster-io/muster/issues/131)) ([e6cc135](https://github.com/muster-io/muster/commit/e6cc13538e68751a68cd3b056c9b68ab2af8269c))
* **auth:** explain that sign-in needs https or localhost ([#319](https://github.com/muster-io/muster/issues/319)) ([9f81cf4](https://github.com/muster-io/muster/commit/9f81cf4c741bddf82715a45769059a14d506752a))
* **db:** ignore every PG environment variable when connecting ([#320](https://github.com/muster-io/muster/issues/320)) ([10003ab](https://github.com/muster-io/muster/commit/10003abbfd829c0d58088241b16fed27940bcaa3))
* **delivery:** log the status and masked error of a failed delivery attempt ([#278](https://github.com/muster-io/muster/issues/278)) ([518a1af](https://github.com/muster-io/muster/commit/518a1af778d72aa9f4a0446efa3cf7087b7d2021))
* **delivery:** take alert group and delivery locks in one order ([#205](https://github.com/muster-io/muster/issues/205)) ([4892e6b](https://github.com/muster-io/muster/commit/4892e6b19aa5b166231e240d41c97156e73a9959))
* **delivery:** treat a database error after the interactive budget ends as the end of the context ([#213](https://github.com/muster-io/muster/issues/213)) ([b3258b6](https://github.com/muster-io/muster/commit/b3258b6a4a128e8c6ceef4f8884b543e26c6fcd7))
* **deps:** update go modules (non-major) ([#87](https://github.com/muster-io/muster/issues/87)) ([9ff94e6](https://github.com/muster-io/muster/commit/9ff94e67a109e6f3c71e95498761351da6f7017f))
* **deps:** update react to v19.3.1 ([#282](https://github.com/muster-io/muster/issues/282)) ([9f61cd7](https://github.com/muster-io/muster/commit/9f61cd767b099cf0cbc8173a6583287287966799))
* **deps:** update tanstack ([#283](https://github.com/muster-io/muster/issues/283)) ([dfbac9a](https://github.com/muster-io/muster/commit/dfbac9abbdadc74ff896d4320c0e5d85a3c627cd))
* **groups:** group firing alerts that have no alert group ([#141](https://github.com/muster-io/muster/issues/141)) ([22446ff](https://github.com/muster-io/muster/commit/22446ffc10fb02d898102d02bae04acc17c43bcc))
* **helm:** sleep before shutdown in the chart and require Kubernetes 1.30 ([d33501a](https://github.com/muster-io/muster/commit/d33501a7577b8c205af348402ef9a08f38c3c1a8))
* **ingest:** release an integration claimed while the worker stops ([#277](https://github.com/muster-io/muster/issues/277)) ([a40673e](https://github.com/muster-io/muster/commit/a40673eaedf9bd9a2a3250a8466707503fffcb44))
* **integrations:** tell users to keep a catch-all route after the muster route ([#127](https://github.com/muster-io/muster/issues/127)) ([d516550](https://github.com/muster-io/muster/commit/d51655096f3921a9c7339dce57937c0e35d3dccf))
* **internalalerts:** keep destination names current after a rename ([#187](https://github.com/muster-io/muster/issues/187)) ([8c10266](https://github.com/muster-io/muster/commit/8c102666b6a54834131d91952f3b331f8d18b61e))
* **keyring:** keep the keys of superseded bridge buttons in use ([#254](https://github.com/muster-io/muster/issues/254)) ([07254bb](https://github.com/muster-io/muster/commit/07254bba501e4efc2bb3742629f6fb33c61c1dfb))
* **links:** refuse renaming a lookup table in use and record the lookup limits ([#155](https://github.com/muster-io/muster/issues/155)) ([47c3d03](https://github.com/muster-io/muster/commit/47c3d039ba6c2a8236851008b8baf759c6b64391))
* **links:** yield no explore link without an environment or cluster label ([#162](https://github.com/muster-io/muster/issues/162)) ([4d70a81](https://github.com/muster-io/muster/commit/4d70a81d4c4782747e378895457f510c0c86c4b3))
* **mattermost:** fall back to ephemeral_text when a press answer cannot be posted ([#163](https://github.com/muster-io/muster/issues/163)) ([0630ba4](https://github.com/muster-io/muster/commit/0630ba4a82b82467cc353b65f3b13e6aceb09e64))
* **metrics:** create the ingest, template error and delivered series at zero ([#227](https://github.com/muster-io/muster/issues/227)) ([e0bcbab](https://github.com/muster-io/muster/commit/e0bcbab30e544085976c09aa503ec24dcbc99cc4))
* **slack:** cut the network connection of a socket whose closure Slack does not answer ([#349](https://github.com/muster-io/muster/issues/349)) ([427a928](https://github.com/muster-io/muster/commit/427a9285e27db70efcfb0d51743be2f2c2527509))
* **slack:** keep a socket whose hello came before a disconnect and match envelope records by id ([#346](https://github.com/muster-io/muster/issues/346)) ([2e815c9](https://github.com/muster-io/muster/commit/2e815c91e323ab054ae7dda3d60de98056997eca))
* **slack:** refuse link codes with messages_tab_disabled and neutralize template mentions in mrkdwn previews ([#332](https://github.com/muster-io/muster/issues/332)) ([562400b](https://github.com/muster-io/muster/commit/562400b78da77468d23c90fea5446698668471c2))
* **slack:** say one slack app per connection in the shared-app hint ([#334](https://github.com/muster-io/muster/issues/334)) ([488ca84](https://github.com/muster-io/muster/commit/488ca8427177dbe6ee0562b7bf035cc8fe6ed746))
* **telegram:** reschedule edits during a press and cap concurrent updates ([#185](https://github.com/muster-io/muster/issues/185)) ([806eaa6](https://github.com/muster-io/muster/commit/806eaa6475c10dbf806426a6dd73d28c236f98c3))
* **templates:** give empty text for a missing label or annotation in every template ([#350](https://github.com/muster-io/muster/issues/350)) ([7816566](https://github.com/muster-io/muster/commit/78165665466595a80b52b50ff0eb351246c7fcad))
* **web:** hide repeated check details, label bridge fields in the audit log and stop reading deleted connections ([#279](https://github.com/muster-io/muster/issues/279)) ([30919fb](https://github.com/muster-io/muster/commit/30919fb56a86a84ed248b85545805ebe147cbeb9))


### Performance Improvements

* **ingest:** process alertmanager groups of an integration in parallel ([#164](https://github.com/muster-io/muster/issues/164)) ([1dd0dc7](https://github.com/muster-io/muster/commit/1dd0dc7d9985c645ebc24ca7a4da2f1bef97f864))
* **web:** split the bundle by route and name every audited field ([#124](https://github.com/muster-io/muster/issues/124)) ([93be32d](https://github.com/muster-io/muster/commit/93be32de0d801fd2238e3aacc5f86f2e236e141e))


### Dependencies

* add script that syncs story contracts to GitHub issues ([53725fc](https://github.com/muster-io/muster/commit/53725fc5c721ad22fa572ac9757bfa4aba594447))
* **deps:** update go to 1.27.3 ([#343](https://github.com/muster-io/muster/issues/343)) ([118d5de](https://github.com/muster-io/muster/commit/118d5defb7e403a5589e482a632c2f57476cb5af))
* **deps:** update the go toolchain to 1.27.2 ([#171](https://github.com/muster-io/muster/issues/171)) ([ff03966](https://github.com/muster-io/muster/commit/ff039666d3127a0953d58151f7ec84507d0e15f6))

<!-- Maintained by release-please from the conventional commits of merged pull requests. Do not edit by hand. -->
