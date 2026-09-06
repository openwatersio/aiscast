# SDR Receivers for AIS Reception

Research notes for a volunteer AIS receiving-station guide. Scope: software-defined radios that deliver IQ (or demodulated FM audio) at 161.975 / 162.025 MHz so that AIS-catcher can do the demodulation and decoding in software. Compiled 2026-08-22; prices are as listed on that date.

## Bottom line

**Buy an RTL-SDR Blog V3 (~$35, dongle only) and spend the rest of your money on antenna height.** Everything below is elaboration on that sentence.

The three claims that matter:

1. **Sensitivity differences between SDR classes are small; siting differences are enormous.** AIS-catcher's own docs put it bluntly: "Raising the antenna does more for your range than any amplifier or premium receiver — a clear view of the water matters more than gain." ([ais-basics.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/ais-basics.md))
2. **AIS is a narrow, undemanding signal.** Two 9,600 bit/s GMSK channels 50 kHz apart. There is nothing here that an 8-bit, 2 MHz-wide RTL-SDR cannot capture perfectly. Wide bandwidth, 14-bit ADCs and 10 MSPS buy you nothing on AIS.
3. **The decoder matters more than the radio.** AIS-catcher's default coherent model recovers "a factor 2 - 3" more messages than the non-coherent FM-discriminator approach used by older decoders on the author's home station. ([model.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/model.md)) A cheap dongle with a good decoder beats an expensive one with a bad decoder.

Spend more only in these cases, in this order: (a) a better antenna and a shorter/better coax run; (b) a SAW-filtered front end **if** you live near strong pager, broadcast-FM or LTE transmitters; (c) a dedicated hardware receiver like the dAISy-catcher if you want plug-and-play with no ppm calibration and no thermal drift. A general-purpose Airspy or SDRplay is not, on the evidence below, worth buying *for AIS*.

### Recommendation ladder

| Tier | Build | Receiver cost |
|---|---|---|
| Baseline | RTL-SDR Blog V3, dongle only, with an antenna you already have | $34.95 |
| Starter kit | RTL-SDR Blog V3 + dipole antenna kit | $44.95–49.95 |
| Serious feeder | V3 + Uputronics 162 MHz SAW preamp (bias-tee powered) + outdoor VHF antenna | ~$35 + ~£44/$59 + antenna |
| High-RF-noise site | Add the $16.95 FM band-stop filter, or consider the V4L | +$16.95 |
| Plug-and-play / remote site | Wegmatt dAISy-catcher | $149.00 |
| Enthusiast / already owned | Airspy Mini, Airspy HF+ Discovery, SDRplay, HydraSDR — all supported natively | — |
| Avoid | Any no-TCXO generic dongle; HackRF One as a *purchase* for AIS | — |

---

## 1. What AIS actually demands of a receiver

| Property | Value | Source |
|---|---|---|
| Channel A (AIS 1) | 161.975 MHz | [ais-basics.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/ais-basics.md) |
| Channel B (AIS 2) | 162.025 MHz | same |
| Channel C / D (long range, satellite) | 156.775 / 156.825 MHz | same |
| Channel separation A↔B | 50 kHz | derived |
| Modulation | GMSK, 9,600 bit/s | same |
| Class A TX power | 12.5 W | same |
| Class B TX power | 2–5 W | same |

Consequences for hardware selection:

- **One dongle receives both channels.** AIS-catcher tunes a single radio to a 162.000 MHz centre frequency and demodulates A and B simultaneously from the same IQ stream ([input/overview.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/input/overview.md)). The default RTL-SDR sample rate of 1536K captures ±768 kHz — roughly 30× more spectrum than the 50 kHz you need. Even the fallback low-CPU rate of 288K is ample.
- **Dual/quad/coherent dongle products are irrelevant to AIS.** KrakenSDR (5-channel coherent, $750, [rtl-sdr.com store](https://www.rtl-sdr.com/store/)) and similar exist for direction finding and beamforming. There is no second channel to assign — both AIS channels already fit in one capture. The only legitimate reason to run a second dongle is to cover the *long-range* C/D channels at 156.8 MHz at the same time as A/B (see §7).
- **Dynamic range demands are modest.** Class A transmitters are 12.5 W and bursty. The problem at a typical shore station is not weak-signal sensitivity but *nearby out-of-band interference* — broadcast FM at 88–108 MHz, DAB, pagers, LTE. This is why front-end filtering, not ADC bit depth, is where extra money pays off.

---

## 2. The RTL-SDR family (the community default)

### 2.1 The 2026 model landscape

Prices from the [official RTL-SDR Blog store](https://www.rtl-sdr.com/store/) as of 2026-08-22.

| Model | Tuner | TCXO | Bias tee | Price (USD) | 2026 status |
|---|---|---|---|---|---|
| **RTL-SDR Blog V3** (dongle only) | R820T2 / R860 | 1 ppm | Yes | **$34.95** | In stock, stable production |
| RTL-SDR Blog V3 + dipole kit | R820T2 / R860 | 1 ppm | Yes | $44.95–49.95 | In stock |
| RTL-SDR Blog V4 (dongle only) | R828D | 1 ppm | Yes | $44.95 | **EOL, out of stock** |
| RTL-SDR Blog V4 + dipole kit | R828D | 1 ppm | Yes | $59.95 | **EOL**, final batch |
| RTL-SDR Blog V4L "Lite" | R828S | 1 ppm | Yes (4.5 V, LED) | $37.95 | New (Aug 2026), China warehouse only |
| Nooelec NESDR SMArt v5 | R820T2 / R860 | 0.5 ppm | **No** | $41.95–44.95 | Integrated heatsink |
| Nooelec NESDR SMArTee v2 | R820T2 | 0.5 ppm | **Yes** | $41.95 | In stock |
| Nooelec NESDR Nano 3 | R820T2 | 0.5 ppm | No | $44.95 | In stock |
| Nooelec NESDR Mini 2+ | R820T2 | 0.5 ppm | No | $41.95–47.95 | Plastic body, MCX antenna |
| Nooelec NESDR SMArTee XTR | E4000 | 0.5 ppm | Yes | $47.95 | Extended tuning range |
| ShipXplorer AIS dongle | RTL-SDR + LNA + SAW bandpass | Yes | n/a | ~$67–85 | Patchy stock |
| Generic no-name DVB-T stick | R820T2 clone | none | No | ~$10–20 | AliExpress/Amazon |

Nooelec prices from [nooelec.com](https://www.nooelec.com/store/sdr/sdr-receivers.html); note their category listing and individual product pages disagree by a few dollars on several models, so verify before quoting. Among Nooelec's line **only the SMArTee models have a bias tee** — the SMArt v5, Mini 2+ and Nano 3 do not, which is the deciding spec if you plan a mast-head preamp.

An **RTL-SDR Blog V5 is "in the early stages of development"** with "no further news expected until 2027" ([rtl-sdr.com news](https://www.rtl-sdr.com/category/news/)), so the V3 is likely to remain the stable option for the life of this guide.

### 2.2 V3 vs V4 vs V4L — the AIS-relevant differences

**The V4's headline change is a filtered front end, and it costs sensitivity at VHF.** RTL-SDR Blog's own release post says the V4 swapped the R860 for the R828D specifically to get "three switchable inputs, enabling filtering across HF, VHF, and UHF bands through a triplexer circuit," and adds switchable notches covering "broadcast AM, broadcast FM and DAB," reducing those "about an additional 5-10 dB." The cost is stated plainly in the same post: **"Due to the increased filtering there can be an average of 2-3 dB less sensitivity on some bands,"** particularly VHF and UHF. ([RTL-SDR Blog V4 Dongle Initial Release](https://www.rtl-sdr.com/rtl-sdr-blog-v4-dongle-initial-release/))

The notches do not target 162 MHz. But 162 MHz is inside the *VHF triplexer leg*, which is exactly where the insertion loss lands. For an AIS station, the V4 trades a couple of dB of the thing you want (sensitivity at 162 MHz) for rejection of bands you may not have a problem with.

The notches do not target 162 MHz — they cover AM, FM (88–108 MHz) and DAB (Band III, 174–240 MHz). Be careful about over-claiming here: **no public measurement of V4 insertion loss at 162 MHz exists.** What is documented is the vendor's own 2–3 dB figure and the fact that AIS sits in the same triplexer leg as the two bands being notched.

**Community reports are mixed but lean toward the V3.** In the AIS-catcher maintainer's own side-by-side survey, the V3 is "probably the standard dongle by which others are measured. Provides decent results, especially for the price," while one user reports "I was disappointed to discover that the V4 unit I have gives worse results than the V3 dongle... it's supposed to be better, according to the manufacturer." Another user in the same thread found the V4 "has been working well for me." ([Discussion #333, SDR Receiver Brief Survey and Side-by-Side Comparisons](https://github.com/jvde-github/AIS-catcher/discussions/333))

**The ADS-B community has run this comparison far more rigorously, and found the same shape.** On FlightAware, charted measurements show the V4 has *lower sensitivity* at 1090 MHz than the V3 but *significantly better out-of-band rejection*. The consensus there: "it's a better general coverage receiver, but for ADSB, the V3 is still probably better," and the V4 "might be a good option if there are strong out of band signals which would normally need heavily filtering." ([FlightAware: RTL-SDR Blog V4 dongle released](https://discussions.flightaware.com/t/rtl-sdr-blog-v4-dongle-released/89198)) That is a different frequency, but it is the same trade-off the vendor describes, measured independently.

**The V4 also had a long driver tail, and its failure mode is silent.** Distribution `librtlsdr` packages did not include V4 support, so AIS-catcher users saw *zero* messages until they rebuilt from source. The maintainer's explanation: "the new drivers that come with the distributions but unfortunately these do not include the changes for the V4," and "unless RTL-SDR sorts out their driver package it remains a bit cumbersome." Multiple users reported swapping a working V3 for a V4 and receiving nothing at all, "not even from the strong base station near their house." ([Discussion #303](https://github.com/jvde-github/AIS-catcher/discussions/303)) On Windows the symptom was "PLL not locked" errors until `rtlsdr.dll` was replaced with the RTL-SDR Blog fork's build ([Discussion #156](https://github.com/jvde-github/AIS-catcher/discussions/156)); on macOS, "No devices found" when built against Radioconda's `librtlsdr`, fixed with `brew install librtlsdr cmake` ([Discussion #500](https://github.com/jvde-github/AIS-catcher/discussions/500), January 2026). Osmocom mainline has since added V4 support ([V4 release post update](https://www.rtl-sdr.com/rtl-sdr-blog-v4-dongle-initial-release/)), but the episode illustrates why the boring option wins for a 24/7 unattended station.

**The V4 is now discontinued.** The store lists it as "now EOL" with both warehouses out of stock. The reason is supply, not design: Rafael Micro stopped making the R828D, RTL-SDR Blog burned through two stockpiles, and the last third-party reels turned out to be counterfeit — "they had put legitimate chips in the first 100 spaces in the reel, then for the rest they had lasered the R828D logo on a totally different chip with the same package size." ([RTL-SDR Blog V4 End Of Line](https://www.rtl-sdr.com/rtl-sdr-blog-v4-end-of-line/), [RTL-SDR Blog V4L (Lite) Now Available for Purchase](https://www.rtl-sdr.com/rtl-sdr-blog-v4l-lite-now-available-for-purchase/), August 2026)

**The V4L is arguably the better AIS dongle of the two, but it is too new to recommend.** It uses the Rafael R828S, "a variant with one fewer RF input," so the front end is *diplexed* (HF vs VHF/UHF) rather than triplexed, and **it has no notch filters at all** — the R828S lacks the open-drain pin the V4 used to drive them. The post notes this "lack of filtering comes with a minor upside: improved sensitivity due to less filtering losses on the front end," with "identical intermodulation performance to the V3 model" on the two-tone test. It keeps the 1 ppm TCXO, 4.5 V bias tee with LED, improved power supply and PCB layout, and the HF upconverter, at $37.95.

The catches: the store warns it "requires updating to the latest drivers to work" and that "not all applications work with the Blog V4L yet"; the announcement notes "The official Osmocom drivers do not yet support it" (with a later update saying support was added), so expect the same downstream distro-package lag that plagued the V4. It is also China-warehouse-only at the moment, and the R828S stockpile is expected to last roughly a year. Worth revisiting; not what you put on a roof today.

**Verdict: the V3 is the de facto default for AIS in 2026.** It is what the AIS-catcher maintainer used to develop and validate the software — "For most of our testing, we have used the RTL-SDR v3 dongle where in principle no frequency correction is needed as deviations are guaranteed to be small" ([troubleshooting.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/troubleshooting.md)) — it is the reference device in his published benchmark tables, it is in stock at the lowest price of the family, and every driver on every distribution supports it without argument.

### 2.3 Why TCXO matters — concretely

At 162 MHz, **1 ppm = 162 Hz** of frequency error ([troubleshooting.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/troubleshooting.md)).

| Oscillator | Error | Error at 162 MHz |
|---|---|---|
| 1 ppm TCXO (Blog V3/V4/V4L) | ±1 ppm | ±162 Hz |
| 0.5 ppm TCXO (Nooelec NESDR) | ±0.5 ppm | ±81 Hz |
| Generic crystal, measured initial offset | **44 ppm** | **~7.1 kHz** |
| Generic crystal, measured 30-min warm-up drift | **~6–7 ppm** | **~1.0–1.1 kHz, and moving** |
| Generic crystal, worst case | 100 ppm | ~16.2 kHz |

The two measured rows come from RTL-SDR Blog's own before/after test of a TCXO-modified dongle: a standard dongle showed a 44 ppm initial offset and roughly 6–7 ppm of drift over 30 minutes from cold, while the TCXO version needed no correction and showed "almost zero drift... (<<1 PPM)." ([Review of the TCXO Modified RTL-SDR Dongle](https://www.rtl-sdr.com/review-tcxo-modified-rtl-sdr-dongle/))

For context, AIS channels are 25 kHz wide. A 7 kHz offset eats a large fraction of that, and the fact that it *moves* over the first half hour is the part software calibration cannot fix: `kalibrate-rtl` finds the static offset, not the thermal one.

AIS-catcher's tolerance: "Deviations between -3 and +3 will usually not impact reception quality so for modern dongles with frequency stabilization no action is required." ([troubleshooting.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/troubleshooting.md))

So a TCXO dongle needs no `-p` correction at all. A generic stick will be tens of ppm off — well outside the tolerance band — and, worse, will *move* as it warms up, so a single `-p` value calibrated cold is wrong by the afternoon. A station operator on Discussion #75 documented daily PPM swings of 0 to ±1.4 with ambient temperature even on a decent dongle, and 2–3 ppm/hour drift before calibration. ([Discussion #75](https://github.com/jvde-github/AIS-catcher/discussions/75))

**AIS-catcher has a mitigation, and it is on by default.** `-go AFC_WIDE on` is "a relatively new model (per v0.48) that is less sensitive to frequency drift" and is "the default model in recent releases"; it can be turned off with `-go AFC_WIDE off`. ([troubleshooting.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/troubleshooting.md)) Field testing on Discussion #75 found AFC_WIDE "demonstrated tolerance for ±6 PPM deviation with minimal message loss, extending to ±10 PPM acceptability."

This genuinely rescues cheap dongles — but "±10 ppm tolerated" is not "±60 ppm tolerated," and the $20 you save on a no-name stick buys nothing except an afternoon with `kalibrate-rtl`. Buy the TCXO.

**Calibration workflow if you need it** (from [Discussion #75](https://github.com/jvde-github/AIS-catcher/discussions/75) and [troubleshooting.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/troubleshooting.md)):

1. Get a rough figure with [`kalibrate-rtl`](https://github.com/steve-m/kalibrate-rtl) or `rtl_test -p60`.
2. Run AIS-catcher with the web viewer (`-N 8100`) and watch the *frequency shift* plot, which reports the correction the decoder had to apply, in ppm.
3. Adjust `-p` iteratively until the hourly frequency-shift plot sits on the zero line, "reasonably stable PPM equally above and below zero."
4. Observe over several days, because ambient temperature moves it.
5. Base-station transmissions are better calibration references than vessels — they are fixed and their oscillators are better.

### 2.4 Nooelec

Nooelec's NESDR line is the main alternative to RTL-SDR Blog and is competitive on paper — 0.5 ppm TCXO versus 1 ppm, at similar prices ([nooelec.com](https://www.nooelec.com/store/sdr/sdr-receivers.html)).

- **NESDR SMArTee v2** ($41.95) — the only one to consider if you want a bias tee for a mast-head preamp. Aluminium case.
- **NESDR SMArt v5** ($41.95–44.95) — black brushed aluminium with an integrated custom heatsink, 0.1–1750 MHz. Good thermal design, **no bias tee**.
- **NESDR Nano 3** ($44.95) and **Mini 2+** ($41.95–47.95) — no bias tee; the Mini 2+ has a plastic body and ships with an MCX antenna plus SMA adapter.

The reason to default to the Blog V3 anyway is not specification but ecosystem: it is the device in the AIS-catcher benchmark tables and the one every troubleshooting thread assumes. A NESDR SMArTee v2 is a perfectly good AIS dongle; it is just not the one people will be able to compare notes with you about. It is also $7 more than a V3 for a TCXO improvement (0.5 vs 1 ppm) that is well inside AIS-catcher's ±3 ppm don't-care band.

### 2.5 Generic no-name dongles

~$10–20 for an RTL2832U + R820T2 clone. Skip them for a permanent station:

- No TCXO — tens of ppm error and continuous thermal drift (§2.3).
- Unknown/absent shielding, which matters because the failure mode at a shore station is interference, not thermal noise.
- No bias tee, so no mast-head LNA without an inline injector.
- No SMA — often MCX, needing an adapter.
- Counterfeit and relabelled tuner chips are endemic in this market; RTL-SDR Blog discontinued the V4 partly *because* of counterfeit R828D supply ([V4L post](https://www.rtl-sdr.com/rtl-sdr-blog-v4l-lite-now-available-for-purchase/)).

They are fine for a weekend test to find out whether your location hears anything at all.

### 2.6 Front-end filtering: the one upgrade with measured evidence

The AIS-catcher maintainer ran a controlled 60-second comparison on a high-performing shared antenna system at the Meteotoren, Scheveningen (`-gr rtlagc on -T 60 -v 60`):

| SDR | Run 1 (msgs/60s) | Run 2 (msgs/60s) |
|---|---|---|
| RTL-SDR Blog V3 | 1061 | 1255 |
| ShipXplorer AIS dongle | 1372 | 1315 |

"The ShipXplorer AIS dongle, as far as I can see, is an RTL-SDR with an additional SAW filter (TA0395A). The two sets of runs suggest some advantages of using a dongle with a filter." ([validation.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/validation.md))

That is roughly +10–25% for a narrowband SAW filter in front of an otherwise identical receiver. Note the caveat in the same document: swapping the site's Yagi for a standard antenna at slightly lower height dropped the count from ~1300 to below 800. **The filter is worth ~20%; the antenna and its height are worth ~40%+.**

Counter-evidence on *wideband* amplification, from the same maintainer's survey ([Discussion #333](https://github.com/jvde-github/AIS-catcher/discussions/333)):

- **Uputronics AIS SAW-filtered preamp**: "No improvement observed with RTL-SDR V3." One unit appeared defective and made reception worse.
- **RTL-SDR Wideband LNA** ($19.95): "Probably won't help unless your station is in an RF-quiet area"; near strong transmitters "reception will almost certainly be worse." It degraded his reception.

The lesson: **a filter removes interference; an amplifier amplifies it too.** Add a filtered preamp only at the mast head to overcome a long coax run, and only after you have evidence that coax loss (not local noise) is your limit. If your coax run is short, an LNA is more likely to hurt than help.

**RTL-SDR Blog sells no AIS-specific product.** There is no filtered AIS dongle, no marine bundle, nothing tuned for 162 MHz in their [store](https://www.rtl-sdr.com/store/). Their relevant parts are generic: the Wideband LNA ($19.95, 50 MHz–4 GHz, <1 dB NF, bias-tee powered — no AIS selectivity whatsoever) and the Broadcast FM Band-Stop Filter ($16.95, >50 dB rejection of 88–108 MHz), which *is* genuinely useful in front of a V3 if you live near an FM transmitter.

**The purpose-built option is the Uputronics 162 MHz AIS Filtered Preamplifier**, which is the community standard for a serious feeder:

| Spec | Value |
|---|---|
| Amplifier | MiniCircuits PSA4-5043+ |
| Filter | 161 MHz SAW bandpass, ~1.4 dB insertion loss, passband 159–163 MHz |
| Gain | 20–22 dB @ 161 MHz |
| Noise figure | 0.78 dB |
| Power | 5 V @ 50 mA via **bias tee** or USB-C |
| Connectors | SMA female both ends; high-pass filter blocks broadcast FM; power LED |

Prices: **£44.40 inc. VAT** at [The Pi Hut](https://thepihut.com/products/162mhz-ais-filtered-preamplifier) (in stock), **$59.00** at [Wegmatt](https://shop.wegmatt.com/products/uputronics-filtered-preamplifier-for-ais) (sold out at time of writing); also carried by [Airspy.US](https://v3.airspy.us/product/upu-fp162s/) and [Tindie](https://www.tindie.com/products/astuder/uputronics-filtered-preamplifier-for-ais/).

The reason this pairs so neatly with the V3: **the V3's built-in bias tee powers the preamp over the coax**, so a mast-head amplifier needs no separate power injector or DC run. V3 + Uputronics 162 MHz preamp at the mast head is the standard serious-AIS-feeder build. (Note the contrary data point in §2.6 above: the AIS-catcher maintainer saw no improvement from one, and had a second unit that was defective. Buy it to solve a diagnosed problem — a long coax run or a measured local interferer — not on spec.)

Operators sourcing bare 162 MHz SAW filters for DIY builds have a long-running thread at [Discussion #198](https://github.com/jvde-github/AIS-catcher/discussions/198).

---

## 3. Higher-end SDRs — is there measurable benefit for AIS?

### 3.1 Coverage and specifications

| Device | Coverage | ADC | Sample rates | Covers 162 MHz? | Price (USD) |
|---|---|---|---|---|---|
| RTL-SDR Blog V3 | 500 kHz–1.75 GHz (+ direct sampling HF) | 8-bit | up to ~2.4 MSPS | Yes | $34.95 |
| Airspy Mini | 24–1700 MHz | 12-bit @ 20 MSPS | 3 / 6 / 10 MSPS | Yes | **$99** (list $119) |
| Airspy R2 | 24–1700 MHz | 12-bit @ 20 MSPS, 10.4 ENOB | 10 MSPS (2.5 experimental) | Yes | **$169** (list $199) |
| Airspy HF+ Discovery | **0.5 kHz–31 MHz and 64–260 MHz** | Σ∆ up to 36 MSPS, 18-bit DDC | up to 768 ksps | **Yes** | **$169** (list $199) |
| SDRplay RSP1B / RSPdx-R2 | 1 kHz–2 GHz | 14-bit | up to 10 MSPS | Yes | ~$130–250, dealer |
| HackRF One | 1 MHz–6 GHz | **8-bit** (MAX5864), half-duplex | up to 20 MSPS | Yes | ~$150–330, varies wildly |
| KrakenSDR | 24–1766 MHz, 5× coherent | 8-bit | — | Yes, but pointless for AIS | $750 ([store](https://www.rtl-sdr.com/store/)) |

Airspy Mini specs: "Continuous 24 – 1700 MHz native RX range," "10, 6 and 3 MSPS IQ output," "12bit ADC @ 20 MSPS," "3.5 dB NF between 42 and 1002 MHz," "4.5v software switchable Bias-Tee." ([airspy.com/airspy-mini](https://airspy.com/airspy-mini/))

Airspy R2 specs: "Continuous 24 – 1700 MHz native RX range," "10MSPS IQ output" (2.5 MSPS experimental), "12bit ADC @ 20 MSPS (10.4 ENOB, 70dB SNR, 95dB SFDR)," "3.5 dB NF between 42 and 1002 MHz," 4.5 V software-switched bias tee. ([airspy.com/airspy-r2](https://airspy.com/airspy-r2/))

Airspy HF+ Discovery specs, answering the "does HF+ cover VHF?" question: **yes** — "HF: 0.5 kHz .. 31 MHz" and "VHF: 64 .. 260 MHz," so 162 MHz is comfortably in range. Output is "Up to 660 kHz alias and image free output for 768 ksps IQ." ([airspy.com/airspy-hf-discovery](https://airspy.com/airspy-hf-discovery/)) 660 kHz of usable bandwidth is far more than AIS needs; AIS-catcher's own benchmark runs it at 192K.

**A practical wrinkle nobody mentions:** the Airspy Mini's slowest rate is 3 MSPS and the R2's is 10 MSPS (2.5 experimental). For a 50 kHz-wide signal, that is 2–7× the data rate of an RTL-SDR at 1536K, all of which your Pi has to downsample. The HF+ Discovery is the opposite and much better suited: 192–768 ksps is exactly the right ballpark for AIS, which is presumably why it is the Airspy in AIS-catcher's own benchmark table. If you are going to use an Airspy for AIS on small hardware, use the HF+ Discovery, not the R2.

### 3.2 The published AIS comparisons

**Sensitivity is essentially a wash.** Objective minimum-discernible-signal measurements put RTL-SDR and Airspy devices at very similar MDS, all around −133 dBm. ([Tech-ni-shn Measures RTL-SDR Blog and Airspy Sensitivity](https://www.rtl-sdr.com/tech-ni-shn-measures-rtl-sdr-blog-and-airspy-sensitivity/))

**AIS-catcher's own survey does not find a reason to upgrade.** On the Airspy HF+: roughly **10–15% higher message count** than an RTL-SDR V3, but the verdict is "May not be worth buying as an upgrade," and parameter tweaking (threshold, preamp, `AFC_WIDE`) "showed no measurable difference." It is described as better suited to HF experimentation. ([Discussion #333](https://github.com/jvde-github/AIS-catcher/discussions/333))

**And the result can go the other way.** In the tuning thread, a user comparing an RTL dongle and an Airspy Mini on the *same antenna via a splitter* reported "at least 10% more messages received on the RTL Dongle than the Airspy Mini." The thread's conclusion is that "early RTL Dongles are very good, but very sensitive to proper calibration for AIS reception" — i.e. the spread between devices is smaller than the spread between a tuned and an untuned configuration of the same device. ([Discussion #75](https://github.com/jvde-github/AIS-catcher/discussions/75))

**The one AIS-catcher table that puts three device classes side by side** is the downsampler benchmark. It is explicitly *not* a device comparison — "the runs are performed on different days over different time spans so this does not represent a comparison of devices but you can compare within a column" — but it does show the maintainer running all three, and shows the sample rates each is used at ([samplerate.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/samplerate.md)):

| Downsampler | RTL-SDR @ 1536K | AirSpy HF+ @ 192K | SDRplay RSPdx @ 3072K |
|---|---|---|---|
| `-go DROOP off` | 94,219 | 16,022 | 16,530 |
| `-go DROOP on` (default) | 98,176 (+4.20%) | 16,265 (+1.52%) | 17,190 (+3.99%) |
| `-go SOXR on` | 97,652 (+3.64%) | 16,209 (+1.17%) | 17,049 (+3.14%) |

**Synthesis: no, there is no reliable measurable benefit from an Airspy or SDRplay over an RTL-SDR for AIS.** Reported deltas run from −10% to +15% depending on who measured and how well the RTL-SDR was tuned, against a 3–8× price difference. If you already own an Airspy or an SDRplay, use it — AIS-catcher supports both natively. Do not buy one for AIS.

### 3.3 HackRF One — don't

HackRF One is a wide-coverage half-duplex *transceiver* optimised for frequency range and transmit capability, not receive quality. Its RX chain is a MAX2837 transceiver into a MAX5864 ADC/DAC via an RFFC5072 mixer/synthesiser ([HackRF hardware components](https://hackrf.readthedocs.io/en/latest/hardware_components.html)); the MAX5864 is an **8-bit** converter. For AIS this is the wrong tool:

- **8-bit ADC**, same as an RTL-SDR, so no dynamic-range advantage despite costing many times more — and unlike the Airspy there is no 12-bit-plus-oversampling headroom to trade for it.
- **Mediocre noise figure without an external LNA.** HackRF has no low-noise front end; it is widely reported to need external amplification to reach RTL-SDR-class sensitivity, and adding a wideband LNA reintroduces exactly the interference problems in §2.6.
- **Half-duplex** and comparatively power-hungry for a device meant to run 24/7.
- No TCXO on stock units, so it inherits the whole ppm problem from §2.3.
- Its default AIS-catcher sample rate is **6144K** — 4× the RTL-SDR default and 120× the bandwidth AIS actually occupies, all of which the host CPU must downsample.

AIS-catcher does support it (`-gf LNA [0-40] VGA [0-62] PREAMP [on/off]`, default sample rate 6144K) ([hackrf.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/input/hackrf.md)), so if you already have one, it works. Buying one to receive AIS would be spending 5–10× the price of a V3 for worse results.

### 3.4 Pricing, and what the premium actually buys

Airspy prices are factory-direct from [airspy.us](https://v3.airspy.us/): Airspy Mini **$99** (list $119), Airspy R2 **$169** (list $199), Airspy HF+ Discovery **$169** (list $199), SpyVerter R2 $49. The site notes it is "currently evaluating the effects of the newly imposed tariffs," so treat these as volatile. SDRplay does not publish prices on its own site (403s to automated fetches); check a dealer.

Put that against the evidence in §3.2:

| Device | Price | Best-case reported AIS gain vs V3 | Cost per percent |
|---|---|---|---|
| RTL-SDR Blog V3 | $34.95 | baseline | — |
| Uputronics 162 MHz SAW preamp (added to a V3) | ~$59 | solves coax loss; SAW filtering worth ~+10–25% in the analogous ShipXplorer test | good, *if* you have the problem |
| Airspy Mini | $99 | reported **−10%** in one same-antenna test | negative |
| Airspy HF+ Discovery | $169 | +10–15%, "may not be worth buying as an upgrade" | ~$9/% |
| Wegmatt dAISy-catcher | $149 | "comparable to... a good SDR like the Airspy HF+" at half the power, plus no ppm/thermal work | buys *reliability*, not sensitivity |

The premium SDRs are not bad radios — they are excellent radios being asked to do a job that does not stress them. You are paying for HF coverage, 12–14 bit dynamic range and multi-MHz bandwidth, and AIS uses none of it.

### 3.5 SDRplay and LimeSDR practicalities

**SDRplay** works well with AIS-catcher — it is in the maintainer's own benchmark table (RSPdx @ 3072K) and has a native driver (`-gs`, default 2304K sample rate). The AIS-catcher page is titled "SDRPlay RSP1/RSP1A/RSPDX (**API 3.x**)" ([sdrplay.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/input/sdrplay.md)), which is the practical catch: SDRplay's API is a closed-source binary service you must install and keep running, rather than a distro package like `librtlsdr`. On a headless Pi that is one more thing to break on an OS upgrade. Gain is set via `lnastate` (0–9) and `grdb` (RF gain reduction, 0–59) rather than a single tuner value, so the tuning procedure in §4.2 needs adapting.

**LimeSDR / LimeSDR Mini** has no native AIS-catcher driver; it would go through **SoapySDR**, which the docs explicitly deprioritise: "In general we recommend to use the built-in drivers for supported SDR devices," and SoapySDR "does not signal if the input parameters for the device are not set properly" — hence the `-gu PROBE on` switch to print what actually got applied ([soapysdr.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/input/soapysdr.md)). Combined with Lime's chequered supply history, there is no case for it here. If you own one, it will work via Soapy; don't buy one for AIS.

### 3.6 HydraSDR

Worth knowing about because AIS-catcher added first-class support for it in v0.67 ([what-is-new.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/what-is-new.md)). It is configured exactly like an Airspy — same three gain modes, same `-gd`/`-gm` style settings ([hydrasdr.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/input/hydrasdr.md)). Same conclusion as the Airspy: fine if you have one, not a reason to spend.

---

## 4. AIS-catcher input support

AIS-catcher's own device list, from `AIS-catcher -L` and the CLI reference ([cli.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/usage/cli.md), [input/overview.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/input/overview.md)):

| Input | Selector | Settings flag | Default sample rate | Notes |
|---|---|---|---|---|
| RTL-SDR | `-d:N` / `-d SERIAL` | `-gr` | **1536K** | The default and best-tested path |
| Airspy R2 / Mini | `-d` | `-gm` | device-specific | Three gain modes |
| Airspy HF+ | `-d` | `-gh` | device-specific | AGC only; `PREAMP`, `THRESHOLD` |
| HydraSDR | `-d` | `-gd` | device-specific | Airspy-like |
| HackRF | `-d` | `-gf` | 6144K | `LNA`, `VGA`, `PREAMP` |
| SDRplay (API 3.x) | `-d` | `-gs` | **2304K** | `GRDB`, `LNASTATE`, `AGC`, `ANTENNA` |
| SoapySDR | `-d SOAPYSDR` | `-gu` | 0 (must set) | Build with `-DSOAPYSDR=ON` |
| RTL-TCP | `-t [host] [port]` | `-gt` | — | Remote dongle |
| SpyServer | `-y [host] [port]` | `-gy` | — | Remote Airspy/RTL |
| ZMQ | `-z [format] [endpoint]` | `-gz` | — | Default format CU8 |
| Raw IQ file / stdin | `-r [format] filename` | `-ga` | — | `CF32/CS16/CU8/CS8` |
| WAV file | `-w filename` | `-gw` | — | |
| Serial NMEA | `-e [baud] [port]` | `-ge` | n/a | dAISy, dAISy-catcher, commercial receivers |
| UDP NMEA (server) | `-x [server] [port]` | — | n/a | |
| TCP NMEA | `-t txt host port` | — | n/a | Format `TXT` disables buffering |
| NMEA2000 socketCAN | `-i [interface]` | — | n/a | Linux only |

### 4.1 Sample rates

Supported: anything above 96K. Internally the decoder upsamples to one of `96K, 192K, 288K, 384K, 768K, 1536K, 3072K, 6144K, 12288K`. "There is no efficiency advantage of using other rates than in this list... Ideally, consider using an option from the list as it avoids upsampling (and additional noise)." ([samplerate.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/samplerate.md))

For RTL-SDR specifically:

- **1536K is the default and the right answer** on anything from a Pi 3 upward.
- **2304K** is offered as an option "If your system allows for it" ([troubleshooting.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/troubleshooting.md)). It is *not* the documented default and there is no published evidence it decodes more AIS. It also pushes closer to the RTL2832U's USB throughput ceiling, where sample drops begin. **Stay at 1536K unless you have measured a reason not to.**
- **288K** is the documented escape hatch for hardware that cannot keep up: "In case you observe a high number of lost data, the advice is to run AIS-catcher at a lower sampling rate." Reception is degraded.

Check your USB path can sustain the rate before blaming the radio:

```bash
rtl_test -s 1536000
```

"On some laptops we observed that Windows was struggling with the high volume of data transferred from the RTL SDR dongle to the PC." ([troubleshooting.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/troubleshooting.md))

### 4.2 Gain — `-gr` for RTL-SDR

Syntax: `AIS-catcher [-d serial] -gr [setting value] ...`. Settings and booleans are case-insensitive on the CLI; JSON keys must be lowercase. ([rtlsdr.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/input/rtlsdr/))

| Setting | Type | Default | Range |
|---|---|---|---|
| `tuner` | auto / float | `auto` | 0–50 dB or AUTO |
| `rtlagc` | boolean | `true` | RTL2832U AGC |
| `biastee` | boolean | `false` | 4.5 V on the coax |
| `buffer_count` | integer | 24 | 1–100 FIFO buffers |
| `sample_rate` | integer | 1536K | 0–20,000,000 |
| `bandwidth` | integer | off | 0–1,000,000 (0 = auto) |
| `freqoffset` | integer | 0 | −150 to +150 ppm |

**There are two competing recommendations in the community, and the difference is worth understanding.**

The official starting point in the CLI guide:

```bash
AIS-catcher -gr RTLAGC on TUNER auto -a 192K
```

"It has been reported by several users that adding a bandwidth setting of `-a 192K` can be beneficial so it is worthwhile to try with and without this filter. Finding the best settings for your hardware requires some systematic experimentation whereby one parameter is changed at a time." ([cli.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/usage/cli.md))

The experienced-operator recommendation from [Discussion #75](https://github.com/jvde-github/AIS-catcher/discussions/75) is the opposite on AGC: **turn RTLAGC off and set the tuner gain manually**, because AGC "struggles with short-duration AIS packets" — an AIS burst is over before an AGC loop settles. The tuning method:

1. Set a fixed `tuner` gain and watch the signal-level plot in the web viewer.
2. Reduce gain until vessel reception drops relative to a reference.
3. Increase gain until signals exceed 0 dB — at which point reception degrades from clipping.
4. Settle where "most signals [are] below 0dB with modest overflow acceptable for very close vessels."

Both are defensible; `auto` is the safe default for a station nobody is going to tune, fixed gain is where the last few percent live. `-a 192K` is a genuinely cheap experiment: it narrows the tuner's analogue bandwidth to just what AIS needs, which is the same interference-rejection argument as the SAW filter, for free. One operator in [Discussion #333](https://github.com/jvde-github/AIS-catcher/discussions/333) reported that adding `-a 192K` "seems to have increased the message count substantially."

Equivalents on other radios: `-gm linearity 10` or `-gm lna AUTO vga 12 mixer 12` (Airspy/HydraSDR), `-gh preamp OFF` (Airspy HF+, AGC-only), `-gf lna 16 vga 16 preamp OFF` (HackRF), `-gs lnastate 5` / `grdb` (SDRplay).

### 4.3 Decoder models — `-m`

From [model.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/model.md):

| Model | Name | Use |
|---|---|---|
| `-m 2` | **Default Model** | The default coherent engine. Leave it alone. |
| `-m 4` | Challenger Model | Higher sensitivity, "typically yields a few percent more decoded messages than the default at the cost of additional CPU." Enable via `-go sensitivity_high on` rather than `-m 4`. |
| `-m 0` | Standard (non-coherent) | Scale-down option; ~20% less CPU than... |
| `-m 1` | Base (non-coherent) | FM-discriminator, comparable to rtl-ais / GNUAIS / aisdecoder |
| `-m 3` | FM discriminator input | For pre-demodulated audio input |
| `-m 5` | NMEA text decoder | Auto-selected for `TXT` input |

Why the default is worth its CPU: "The Default Model is the most time and memory consuming but experiments suggest it to be the most effective. In my home station, it improves message count by a factor 2 - 3." The published example run:

```
[AIS engine v0.35]:              38 msgs at 6.3 msg/s
[Standard (non-coherent)]:        4 msgs at 0.7 msg/s
[Base (non-coherent)]:            3 msgs at 0.5 msg/s
```

"Advice is to start with the Default Model, which should run fine on most modern hardware including a Raspberry 4B and then scale down to `-m 0` or even `-m 1`, if needed."

You can benchmark models against each other on one input with `-b` (timing) and `-v` (counts); only the first model's messages are output:

```bash
AIS-catcher -s 1536K -r posterholt.raw -m 2 -m 0 -m 1 -q -b -v
```

### 4.4 Decoder options — `-go`

Full set from the CLI help: `-go Model: AFC_WIDE [on/off] FP_DS [on/off] PS_EMA [on/off] SOXR [on/off] SRC [on/off] DROOP [on/off]` ([cli.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/usage/cli.md)), plus the settings documented in [model.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/model.md).

| Option | Default | What it does | Change it? |
|---|---|---|---|
| `AFC_WIDE` | **on** (default model since v0.48) | Frequency-drift-tolerant demodulation; tolerates ~±6 ppm with minimal loss, up to ±10 ppm | Leave on. Essential on non-TCXO dongles. |
| `DROOP` | **on** (since v0.39) | 3-tap droop compensator for the CIC5 downsampling filter | Leave on — measured **+4.20%** messages on RTL-SDR @1536K for negligible CPU |
| `FP_DS` | off | Fixed-point downsampling | Only on very slow hardware |
| `SOXR` | off | SOX resampler instead of built-in CIC5 | No. +3.64% vs droop-off, but 2.6× the CPU, and *worse* than `DROOP on` |
| `SRC` | off | libsamplerate resampler | Definitely not — 14.8× the CPU of FP_DS |
| `PS_EMA` | off | Phase/power smoothing (EMA) | Undocumented; experimental |
| `sensitivity_high` | off | Switch to the `-m 4` Challenger model | Try it if you have CPU headroom |
| `station_id`, `own_mmsi`, `uuid` | — | Metadata | Set as appropriate |

Downsampler CPU cost on the same recording ([samplerate.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/samplerate.md)):

| Method | Time | Messages |
|---|---|---|
| Built-in (CIC5 + droop) | 320.6 ms | 41 |
| `FP_DS on` | **254.3 ms** | 41 |
| `SOXR on` | 653.7 ms | 41 |
| `SRC on` | 3762.6 ms | 41 |

**The practical takeaway: the defaults are already the right answer.** The only `-go` knob most operators should touch is `sensitivity_high on`.

### 4.5 Squeezing performance out of an RTL-SDR — the checklist

In descending order of measured impact:

1. **Raise the antenna and shorten the coax.** Everything below is worth a few percent; this is worth a factor.
2. **Get `-p` right**, or rely on a TCXO dongle plus `AFC_WIDE` (§2.3).
3. **Try `-a 192K`** — narrows tuner bandwidth to what AIS needs ([cli.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/usage/cli.md)).
4. **Tune the gain systematically**, one parameter at a time: `RTLAGC on/off`, then fixed `TUNER` values 0–50, watching the signal-level plot for clipping above 0 dB (§4.2).
5. **Stay on `-m 2` at `-s 1536K`** with `DROOP on` and `AFC_WIDE on` (all defaults).
6. **Try `-go sensitivity_high on`** if CPU allows — a few percent.
7. **Verify USB throughput** with `rtl_test -s 1536000` before assuming an RF problem.
8. **Only then** consider a SAW-filtered front end (§2.6).

---

## 5. Dedicated AIS hardware (not IQ, but relevant to the comparison)

These are not SDRs — they demodulate in dedicated silicon and emit NMEA over serial — but AIS-catcher ingests them via `-e`, and for many volunteer operators they are the better answer.

| Device | Price (USD) | Interface | Notes |
|---|---|---|---|
| **Wegmatt dAISy-catcher** | **$149.00** (+$39 GPS add-on, +$19 case) | USB or Pi HAT, SMA | Dual-channel, co-developed by the AIS-catcher author and Wegmatt. Sensitivity "better than −120 dBm at 20% packet error rate." Beta testers reported "3-4x more vessel detections" than older dAISy models. ([shop.wegmatt.com](https://shop.wegmatt.com/products/daisy-catcher-high-performance-ais-receiver)) |
| Wegmatt dAISy HAT | from $75 | Pi GPIO HAT | Two-channel, for Raspberry Pi ([shop.wegmatt.com](https://shop.wegmatt.com/products/daisy-hat-ais-receiver)) |
| Wegmatt dAISy 2+ | from $112 | NMEA 0183 / USB | Dual-channel; "about 40% more messages than the cheaper single-channel dAISy" ([shop.wegmatt.com](https://shop.wegmatt.com/products/daisy-2-dual-channel-ais-receiver-with-nmea-0183)) |
| ShipXplorer AIS dongle | ~$67–85 (resellers) | USB (RTL-SDR + LNA + TCXO + SAW bandpass) | The filtered dongle in the Meteotoren test (§2.6). rtl-sdr.com describes it as "an RTL-SDR dongle with AIS modifications (LNA & TCXO)" plus an SMD bandpass filter; reported to perform "comparably to the RTLSDRv3" ([rtl-sdr.com](https://www.rtl-sdr.com/airnav-systems-launch-ais-aggregator-shipxplorer-com/), [shipxplorer.com](https://www.shipxplorer.com/ais-dongle)). AIS-catcher lists it as explicitly supported. No official published price. |
| Comar SLR350NI | — | Ethernet | The commercial receiver at the Meteotoren MarineTraffic station; AIS-catcher on a V3 was in the same message-rate ballpark ([validation.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/validation.md)) |

The AIS-catcher README now recommends the dAISy-catcher first for people who want the SDR software benefits without SDR hardware fiddling: "plug-and-play operation, low power consumption, and precise signal processing." ([README](https://github.com/jvde-github/AIS-catcher/blob/main/README.md))

Configuration is a serial device at 115200 baud with an init sequence that turns on signal-level and frequency-offset reporting:

```bash
AIS-catcher -e 115200 /dev/serial0 -ge init_seq co2,v
```

([daisy-catcher.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/input/daisy-catcher.md))

Wegmatt's co-developer describes results "comparable to running AIS-catcher with a good SDR like the Airspy HF+" at roughly half the power, with precision timestamps, accurate signal levels and AIS-tuned filters; users in the thread report a noise floor with weakest signals around −97 dBm and ducting receptions past 450 miles. ([Discussion #563](https://github.com/jvde-github/AIS-catcher/discussions/563))

For balance, the comparison is not uniformly one-sided: [issue #519](https://github.com/jvde-github/AIS-catcher/issues/519) (March 2026, closed) reports "significantly fewer messages received with dAISy-Hat vs RTL-SDR v4" — that is the older single-channel HAT rather than the dAISy-catcher, but worth knowing.

**When to pick this over a dongle:** you want no ppm calibration, no thermal drift, no USB throughput concerns, and ~1 W instead of ~2–3 W; or you are deploying somewhere you will not be able to service. The trade is $149 versus $35 and losing the ability to use the same hardware for anything else.

---

## 6. Known pitfalls

| Pitfall | Reality | Fix |
|---|---|---|
| **Overheating** | Real. The R820T top hits 85 °C after 10 minutes; internal temperatures reach 70 °C. Heat both drifts frequency and reduces sensitivity ([rtl-sdr.com on cooling](https://www.rtl-sdr.com/cooling-the-rtl-sdr-for-improved-sensitivity/), [thermal camera](https://www.rtl-sdr.com/rtl-sdr-heat-dissipation-as-seen-by-a-thermal-camera/)) | Use the metal-cased Blog V3/V4L (passive cooling by design). Give it air. In a sealed outdoor enclosure, add a heatsink or thermal pad to the case. |
| **USB 3 interference** | Real. USB 3 signalling radiates broadband hash; users report "major interference at specific frequencies when dongles are placed in powered USB 3.0 hubs" ([rtl-sdr.com](https://www.rtl-sdr.com/tip-reduce-radio-interference-rtl-sdr/)) | **Plug the dongle into a USB 2.0 port.** There is no throughput reason to use USB 3 — the RTL2832U is a USB 2.0 device. |
| **USB shield acting as an antenna** | Real. The shield of a USB extension cable couples RFI into the dongle; disconnecting the shield from the dongle's ground "reduced an interfering signal by 10 dB" ([Reducing USB Shield Interference](https://www.rtl-sdr.com/reducing-usb-shield-interference-rtl-sdr-dongles/), [Tip to Reduce Radio Interference](https://www.rtl-sdr.com/tip-reduce-radio-interference-rtl-sdr/)) | Use a short, well-shielded extension with a ferrite; if you see a stubborn birdie, try breaking the shield ground at one end. |
| **Extension cables** | Mixed. Moving the dongle away from a noisy host helps sometimes and does nothing other times; several users report real range loss from cheap extensions ([FlightAware](https://discussions.flightaware.com/t/usb-extension-cable-recommendations/42511)) | Community rule of thumb: **prefer a longer antenna cable over a longer USB cable.** Better still, *shorter coax + longer Ethernet* — put the Pi at the antenna. |
| **Raspberry Pi USB power** | Overstated for modern Pis, but real for Pi Zero and marginal PSUs. A dongle draws ~180 mA; older guidance about 140 mA ports refers to Pi 1 | Use the **official PSU** for your Pi model. A powered hub is only needed for the Zero, for multiple dongles, or when driving a bias-tee LNA. Watch for undervoltage warnings in `dmesg`. |
| **Bias tee left on into a DC-shorted antenna** | Real risk of damaging the dongle | Only `-gr biastee on` when there is actually an LNA up the coax expecting 4.5 V. Blog V4/V4L have an LED to show it ([V4 post](https://www.rtl-sdr.com/rtl-sdr-blog-v4-dongle-initial-release/)) |
| **Direct sampling mode** | Irrelevant and harmful here. Direct sampling is the HF (<28.8 MHz) bypass mode. AIS at 162 MHz goes through the normal tuner path | Never enable it for AIS. If you bought a V4/V4L for its HF upconverter, that's a separate use case. |
| **`-s 2304K` cargo-culting** | Higher sample rate does not mean more AIS. It means more USB throughput, more CPU and more chance of dropped samples | Stay at the 1536K default. Only drop to 288K if `rtl_test` shows loss. |
| **RTLAGC on by default** | Debatable for AIS. AIS bursts are too short for an AGC loop to settle ([Discussion #75](https://github.com/jvde-github/AIS-catcher/discussions/75)) | Try fixed `TUNER` gain against the default `auto`, changing one thing at a time. |
| **Wideband LNA "for more range"** | Usually counterproductive near transmitters ([Discussion #333](https://github.com/jvde-github/AIS-catcher/discussions/333)) | Filter first, amplify only at the mast head and only for long coax. |
| **Driver mismatch (V4/V4L)** | Real and silent — you get *zero* messages, not degraded messages ([Discussion #303](https://github.com/jvde-github/AIS-catcher/discussions/303)) | Confirm with `AIS-catcher -l` that the device enumerates, and `rtl_test` that it tunes. Build `librtlsdr` from source if needed. |
| **Cheap SBC RFI** | The board itself can be the interferer: "these lower cost boards ... can create interference that impacts the radio reception" ([cli.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/usage/cli.md)) | Distance and shielding between SBC and dongle/antenna. |

---

## 7. Multiple receivers and antenna diversity

AIS-catcher runs multiple receivers in one process. Each `-d` (or `-e`/`-x`/`-t`) starts a new receiver block, and subsequent flags apply to it ([input/overview.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/input/overview.md)):

```bash
AIS-catcher -d serial1 -v -d serial2 -c CD -v -N 8100
```

In JSON, the `receiver` array is the recommended structure even for a single device ([json-configuration.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/usage/json-configuration.md)):

```json
{
    "config": "aiscatcher",
    "version": 1,
    "receiver": [
        { "input": "airspy", "serial": "airspy", "airspy": { "sample_rate": "3000K" } },
        { "input": "rtlsdr", "serial": "ais",    "rtlsdr": { "bandwidth": "192k" } }
    ]
}
```

**Be clear about what this is and is not.** AIS-catcher does *not* do coherent diversity combining — there is no maximal-ratio or selection combining of IQ across dongles. What it does is run independent decoders and merge their message streams, with optional de-duplication.

Legitimate uses for a second receiver:

1. **Long-range channels.** `-c CD` retunes a receiver to 156.800 MHz for AIS channels C/D (156.775 / 156.825 MHz), the satellite long-range channels. Because `-c` changes the *tuning frequency*, you need a second radio to cover A/B and C/D simultaneously — this is the one case where a second dongle genuinely adds coverage. ([long-range.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/long-range.md))
2. **Two antennas covering different sectors** — e.g. a Yagi at the shipping lane plus an omni for everything else. Union of coverage, with `unique` removing the overlap.
3. **A GPS receiver alongside the SDR**, so the station position on the map comes from GPS: `AIS-catcher -e 38400 /dev/serial/by-id/usb-...GPS... -x 192.168.1.235 4002`.
4. **Aggregating other stations' NMEA** over UDP/TCP into one feed.

De-duplication (v0.67+), needed whenever streams overlap:

```bash
AIS-catcher -o 1 unique on -u 127.0.0.1 5012 unique on
```

"If a message with the same content (hash) is received multiple times within the interval, only the first is output... Note that this could come at a performance cost that increases with the number of MMSIs." ([message-filtering.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/output/message-filtering.md), [what-is-new.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/what-is-new.md))

**Splitting one antenna to two receivers** is what people actually do for A/B testing — a VHF splitter feeding an RTL-SDR and an Airspy was the method in [Discussion #75](https://github.com/jvde-github/AIS-catcher/discussions/75). Note the splitter costs ~3.5 dB per port, so this is a comparison technique, not a performance upgrade.

**Honest assessment:** for a volunteer station, a second dongle on a second antenna is a much worse investment than raising the first antenna by three metres. The exception is `-c CD`, which is genuinely additive coverage no amount of antenna height gives you.

---

## 8. What a minimal station costs in 2026

AIS-catcher's own estimate: "roughly €60 when starting from nothing, and chances are you already own half of it." ([what-you-need.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/what-you-need.md))

### Tier 1 — get on the air (~$80)

| Item | Price (USD) | Source |
|---|---|---|
| RTL-SDR Blog V3 + dipole antenna kit | $44.95 | [rtl-sdr.com store](https://www.rtl-sdr.com/store/) |
| Raspberry Pi Zero 2 W | $15 | [raspberrypi.com](https://www.raspberrypi.com/products/) |
| Pi PSU + microSD card | ~$20 | |
| Coax already in the dipole kit | — | |
| **Total** | **~$80** | Plus shipping/tax |

The dipole kit extended to ~44 cm (a quarter wave at 162 MHz) at a window facing the water "is enough to see your first ships in a busy area" ([what-you-need.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/what-you-need.md)). Note the Pi Zero 2 W will likely need `-F` or `-s 288K` — "the hardware might not keep up with the high data flow" ([cli.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/usage/cli.md)).

### Tier 2 — the station worth running 24/7 (~$210–320)

| Item | Price (USD) |
|---|---|
| RTL-SDR Blog V3, dongle only | $34.95 |
| Raspberry Pi 5 (4 GB) or Pi 4 | ~$45–60 |
| Official PSU + microSD/SSD | ~$30 |
| Outdoor marine VHF / AIS antenna | ~$40–90 |
| 10 m LMR-400 or RG-213 with connectors | ~$40–60 |
| Mast/mount hardware | ~$20–40 |
| **Total** | **~$210–320** |

The coax choice is not cosmetic ([ais-basics.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/ais-basics.md)):

| Cable | Loss per 10 m at ~160 MHz |
|---|---|
| RG-58 | ~2.0 dB |
| RG-8X | ~1.5 dB |
| RG-213 | ~0.9 dB |
| LMR-400 | ~0.5 dB |

1.5 dB of extra loss is roughly the entire advantage a SAW-filtered dongle bought you in the Meteotoren test. **If the run is long, the cable upgrade beats the receiver upgrade.** Better still, follow the docs' advice: "place the receiver (for example a Raspberry Pi with the SDR dongle) close to the antenna and run a network cable instead — network cables have no signal loss."

### Tier 3 — where extra money actually goes

| Upgrade | Price | Evidence |
|---|---|---|
| More antenna height | Mast/pole cost | 5 m → 10 m takes the horizon from ~25 km to ~29 km ([ais-basics.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/ais-basics.md)) — and clears obstructions, which matters more than the number |
| SAW-filtered dongle (ShipXplorer) | ~$67–85 | ~+10–25% messages in a controlled test ([validation.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/validation.md)) |
| Uputronics 162 MHz SAW preamp at the mast head | ~£44 / $59 | 0.78 dB NF, 20–22 dB gain, powered by the V3's bias tee. Solves *long coax*, not weak signals ([The Pi Hut](https://thepihut.com/products/162mhz-ais-filtered-preamplifier)) |
| FM band-stop filter | $16.95 | Only if you are near a broadcast FM transmitter ([store](https://www.rtl-sdr.com/store/)) |
| dAISy-catcher | $149 | Plug-and-play, −120 dBm @ 20% PER ([Wegmatt](https://shop.wegmatt.com/products/daisy-catcher-high-performance-ais-receiver)) |
| Airspy / SDRplay | 3–8× a V3 | −10% to +15% messages, direction depending on who measured ([#333](https://github.com/jvde-github/AIS-catcher/discussions/333), [#75](https://github.com/jvde-github/AIS-catcher/discussions/75)) — **not recommended for AIS** |
| Wideband LNA | $19.95 | Likely *negative* unless RF-quiet and coax-limited ([#333](https://github.com/jvde-github/AIS-catcher/discussions/333)) |

Expected performance for reference: "a rooftop antenna at 5–10 m typically receives large ships out to 25–30 km (14–16 nmi)" ([what-you-need.md](https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/what-you-need.md)).

---

## 9. Recommended configurations

**Default volunteer station:**

```bash
AIS-catcher -gr RTLAGC on TUNER auto -a 192K -v 10 -N 8100 -u 127.0.0.1 10110
```

RTL-SDR Blog V3, 1536K default sample rate, `-m 2` default model, `AFC_WIDE` and `DROOP` on by default. Web viewer on 8100 for the signal-level and frequency-shift plots you need in order to tune anything.

**After a week of watching the plots**, if the ppm plot is off zero, add `-p <ppm>`; if signal levels rarely approach 0 dB, try fixed gains: `-gr RTLAGC off TUNER 33.3`.

**On a Pi Zero 2 W:**

```bash
AIS-catcher -F -gr RTLAGC on TUNER auto -a 192K
```

and drop to `-s 288K` if that still overruns.

**With CPU headroom to spare:**

```bash
AIS-catcher -gr RTLAGC on TUNER auto -a 192K -go sensitivity_high on
```

---

## 10. Source index

**Primary — AIS-catcher**
- README: https://github.com/jvde-github/AIS-catcher/blob/main/README.md
- Docs site: https://jvde-github.github.io/AIS-catcher-docs/ (source: https://github.com/jvde-github/AIS-catcher-docs)
- What you'll need: https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/what-you-need.md
- AIS basics (frequencies, horizon, coax loss): https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/ais-basics.md
- Input overview, `-c`, multi-device: https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/input/overview.md
- RTL-SDR settings: https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/input/rtlsdr.md
- Airspy / Airspy HF+ / HackRF / SDRplay / HydraSDR / SoapySDR / serial: same directory
- dAISy-catcher: https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/input/daisy-catcher.md
- Models and `-go`: https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/model.md
- Sample rates and downsampler benchmark: https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/samplerate.md
- Troubleshooting (ppm, AFC_WIDE, USB throughput): https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/troubleshooting.md
- Validation (Meteotoren V3 vs ShipXplorer): https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/validation.md
- Long-range C/D channels: https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/advanced/long-range.md
- CLI reference: https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/usage/cli.md
- JSON multi-receiver config: https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/usage/json-configuration.md
- Message filtering / `unique`: https://github.com/jvde-github/AIS-catcher-docs/blob/main/docs/configuration/output/message-filtering.md

**Primary — community threads**
- Discussion #75, tuning an RTL dongle with the graphs: https://github.com/jvde-github/AIS-catcher/discussions/75
- Discussion #156, V4 "PLL not locked" / driver DLL: https://github.com/jvde-github/AIS-catcher/discussions/156
- Discussion #198, sourcing 162 MHz SAW filters: https://github.com/jvde-github/AIS-catcher/discussions/198
- Discussion #303, RTL-SDR V4 driver support: https://github.com/jvde-github/AIS-catcher/discussions/303
- Discussion #333, SDR receiver side-by-side survey: https://github.com/jvde-github/AIS-catcher/discussions/333
- Discussion #500, macOS V4 build/driver: https://github.com/jvde-github/AIS-catcher/discussions/500
- Discussion #563, dAISy-catcher: https://github.com/jvde-github/AIS-catcher/discussions/563
- Issue #519, dAISy HAT vs RTL-SDR V4 message counts: https://github.com/jvde-github/AIS-catcher/issues/519
- FlightAware, V3 vs V4 for ADS-B: https://discussions.flightaware.com/t/rtl-sdr-blog-v4-dongle-released/89198
- FlightAware, USB extension cable recommendations: https://discussions.flightaware.com/t/usb-extension-cable-recommendations/42511

**Primary — vendors**
- RTL-SDR Blog store (prices, EOL status): https://www.rtl-sdr.com/store/ and https://www.rtl-sdr.com/buy-rtl-sdr-dvb-t-dongles/
- RTL-SDR Blog V4 release: https://www.rtl-sdr.com/rtl-sdr-blog-v4-dongle-initial-release/
- RTL-SDR Blog V4 End Of Line: https://www.rtl-sdr.com/rtl-sdr-blog-v4-end-of-line/
- RTL-SDR Blog V4L release (Aug 2026): https://www.rtl-sdr.com/rtl-sdr-blog-v4l-lite-now-available-for-purchase/
- Nooelec SDR receivers: https://www.nooelec.com/store/sdr/sdr-receivers.html
- Airspy Mini specs: https://airspy.com/airspy-mini/
- Airspy HF+ Discovery specs: https://airspy.com/airspy-hf-discovery/
- Wegmatt dAISy-catcher: https://shop.wegmatt.com/products/daisy-catcher-high-performance-ais-receiver
- Wegmatt dAISy HAT: https://shop.wegmatt.com/products/daisy-hat-ais-receiver
- Uputronics 162 MHz AIS filtered preamp: https://thepihut.com/products/162mhz-ais-filtered-preamplifier and https://shop.wegmatt.com/products/uputronics-filtered-preamplifier-for-ais
- ShipXplorer AIS dongle: https://www.shipxplorer.com/ais-dongle
- Raspberry Pi products: https://www.raspberrypi.com/products/

**Secondary — RTL-SDR Blog articles**
- RTL-SDR vs Airspy sensitivity (MDS ~−133 dBm): https://www.rtl-sdr.com/tech-ni-shn-measures-rtl-sdr-blog-and-airspy-sensitivity/
- TCXO modified dongle review (44 ppm offset, 6–7 ppm drift): https://www.rtl-sdr.com/review-tcxo-modified-rtl-sdr-dongle/
- AIS-catcher announcement: https://www.rtl-sdr.com/ais-catcher-a-dual-band-multiplatform-ais-receiver-for-rtl-sdr-and-airspy-hf-with-multiple-decoding-models/
- ShipXplorer / AirNav AIS aggregator launch (dongle internals): https://www.rtl-sdr.com/airnav-systems-launch-ais-aggregator-shipxplorer-com/
- Cooling the RTL-SDR: https://www.rtl-sdr.com/cooling-the-rtl-sdr-for-improved-sensitivity/
- Thermal camera on RTL-SDR: https://www.rtl-sdr.com/rtl-sdr-heat-dissipation-as-seen-by-a-thermal-camera/
- Reducing USB shield interference: https://www.rtl-sdr.com/reducing-usb-shield-interference-rtl-sdr-dongles/
- Reducing radio interference: https://www.rtl-sdr.com/tip-reduce-radio-interference-rtl-sdr/
- CNX Software on the V4 triplexer: https://www.cnx-software.com/2023/08/17/rtl-sdr-blog-v4-dongle-launched-with-rafeal-r828d-tuner-chip/

## 11. Open gaps for the guide author

- **No rigorous public V3-vs-V4 AIS benchmark exists.** What we have is two conflicting anecdotes in Discussion #333, the vendor's own 2–3 dB admission, and a much better-measured ADS-B comparison at 1090 MHz. Do not present the V3 advantage as measured fact for AIS; present it as the convergence of vendor spec, community sentiment, price and availability.
- **No measured V4 insertion loss at 162 MHz.** Any claim that the DAB notch clips the AIS band is inference, not data.
- **Airspy and SDRplay street prices need a dealer check** — neither vendor publishes prices on their spec pages.
- **Nooelec's own pricing is internally inconsistent** between category listing and product pages; re-verify before publishing.
- **Reddit r/RTLSDR was not consulted** (unfetchable from this environment). The "community default" conclusion rests on AIS-catcher's docs and discussions plus vendor stock status, which is independent support, but a manual Reddit pass would strengthen it.
- **ShipXplorer publishes no official price**; the $67–85 range is from third-party resellers with unreliable stock.
