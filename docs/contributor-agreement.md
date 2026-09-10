# AIS Contributor Agreement

This agreement is between you and Open Water Software, LLC ("us"/"we"). You operate an AIS receiving station and share its data. We operate the AIS network at ais.openwaters.io. By sharing data with us, you accept this agreement.

## Summary

You dedicate the data you share to the public domain. We publish the aggregate under an open license and keep it open, even if the project is sold. You confirm that the data is your station's own reception. You can stop sharing at any time. Changes to this agreement arrive as public pull requests.

## Your dedication

By sharing data from your AIS receiver with us, you dedicate your receptions to the public domain under [CC0](https://creativecommons.org/publicdomain/zero/1.0/). That is the entire grant.

Anyone may use your receptions for any purpose, including commercial use. We hold the same rights to your receptions as everyone else. You can share the same data to other networks.

The dedication covers everything you send. If it includes your own vessel's sentences (`!AIVDO`), we publish them like any other reception. We then identify your station by that MMSI. Anyone who reads the data can see your position. Configure your forwarder to drop own-vessel sentences if you do not want to share them.

The dedication is permanent for data already released. If you stop sharing, we release no new data from your station. Published data stays published.

## Our commitments

- **The aggregate stays open.** We publish the aggregate dataset from all stations' receptions under the [Open Database License (ODbL)](https://opendatacommons.org/licenses/odbl/). Anyone may use the dataset, including for commercial purposes. What they build from it must stay open under the same terms. We never relicense it. We never put it behind terms that forbid redistribution. We never grant anyone an exclusive license to it. A buyer of Open Water Software, LLC holds the same rights to the data as everyone else. Anyone can mirror the public archive and operate a successor network. We may withhold specific vessels from publication under our [privacy policy](privacy-policy.md).
- **We charge for hosted access.** The data is free. We charge for the hosted service: reliable streaming at scale, history queries, and an SLA. That revenue pays for the infrastructure. Contributors get the commercial tier at no cost.

## Your representations

We ask little of you, deliberately:

1. Send only data that your station received over the air. Do not send fabricated traffic. Do not send replayed data or third-party feeds as your own reception.
2. You have the right to send the data to us. Reception and sharing of broadcast AIS is lawful in the jurisdictions that matter for this network. If your local law is different, you must check it yourself.

## Leaving

Stop sharing whenever you want. Notice is not required. Data already released under ODbL or CC0 stays released, per your dedication above.

## Changes to this agreement

This document lives in the [aiscast](https://github.com/openwatersio/aiscast) public repository. We revise this agreement only by pull request there. Changes take effect 30 days after the merge.

We do not collect contact information from contributors, so watch the repository to hear about changes. If you continue to share after a change takes effect, you accept it. If you do not accept it, stop sharing, and the leaving terms above apply. Changes apply only to data sent after they take effect.

## Everything else

Neither side gives a warranty. You are not responsible for data quality, uptime, or continued sharing. We do not promise the network will run forever, though the open archive means it can outlive us.
