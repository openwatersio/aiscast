# The RF Front End: Antenna to Receiver Input

Everything between the air and the SDR's SMA connector. Prices checked August 2026; verify before buying.

The one-sentence version: **height beats everything, the antenna beats the dongle, coax loss is silently expensive, and filters/LNAs are a targeted fix for a specific problem — not a general upgrade.**

---

## 1. What AIS is, on the air

| Property | Value | Source |
|---|---|---|
| AIS 1 (channel 87B) | 161.975 MHz | [Wikipedia: AIS](https://en.wikipedia.org/wiki/Automatic_identification_system) |
| AIS 2 (channel 88B) | 162.025 MHz | same |
| Long-range AIS (AIS 3/4) | 156.775 / 156.825 MHz | [dAISy-catcher manual, §11](https://wegmatt.com/files/dAISy-catcher%20AIS%20Receiver%20Manual.pdf) |
| Modulation | GMSK over FM, BT ≤ 0.4, modulation index 0.5 | [Wikipedia: AIS](https://en.wikipedia.org/wiki/Automatic_identification_system) |
| Bit rate | 9600 bit/s | [USCG NavCen, How AIS Works](https://www.navcen.uscg.gov/how-ais-works) |
| Channel bandwidth | 25 kHz (12.5 kHz variant exists) | [USCG NavCen](https://www.navcen.uscg.gov/how-ais-works) |
| Slot structure | 2250 slots/min/channel, 4500 total | [USCG NavCen](https://www.navcen.uscg.gov/how-ais-works) |

The two channels are only 50 kHz apart, which is why a single SDR at ~192 kHz–1.5 MHz sample rate decodes both simultaneously, and why a *narrowband* SAW filter centred on 162 MHz with ~100 kHz of 3 dB bandwidth can pass both (e.g. the [Wegmatt TB0436A](https://shop.wegmatt.com/products/tb0436a-narrowband-saw-filter-for-ais), 90–108 kHz 3 dB BW).

**Transmit power by class** — this sets how hard each target is to hear:

| Class | Power | Access scheme | Practical range | Source |
|---|---|---|---|---|
| Class A | 12.5 W (2 W low-power mode) | SOTDMA | Longest; 20–40+ nm to a good shore station | [Wikipedia](https://en.wikipedia.org/wiki/Automatic_identification_system) |
| Class B/SO ("B+") | 5 W | SOTDMA | Intermediate | [Digital Yacht FAQ](https://digitalyachtamerica.com/sales-faq/what-is-the-difference-between-ais-class-b-class-b-5w-and-class-a/) |
| Class B/CS | 2 W | CSTDMA | ~5–10 nm | [Wikipedia](https://en.wikipedia.org/wiki/Automatic_identification_system) |

There is an 8 dB gap between a Class A and a Class B/CS transmitter. A station that hears Class A ships at 40 nm will typically lose the small Class B boats well inside 15 nm. When you compare two stations' "range," check which class the record was set with.

### What this implies for antenna design

162 MHz means λ = 300/162 = **1.852 m**. That is a big antenna by SDR-hobby standards — a quarter wave is 46 cm, a half wave is 93 cm — but it is *exactly* the marine VHF band, so a huge commercial ecosystem of correctly-sized, weatherproof, marine-grade antennas already exists and is cheap. You should almost never build an AIS antenna from scratch unless you want to.

Vertical polarization, always. Ship antennas are vertical whips; a horizontally-oriented dipole costs you roughly 20 dB of cross-polarization loss. MarineTraffic states the rule flatly: "Dipole antennas must be installed upright, never horizontally" ([MarineTraffic install guide](https://support.marinetraffic.com/en/articles/9552957-how-to-install-an-ais-station)).

### Element lengths at 162 MHz

Free-space λ = 1.852 m. Real conductors are electrically slightly long, so multiply by a velocity factor (~0.95 for wire, ~0.96 for tubing).

| Element | Free space | × 0.95 (wire) | × 0.96 (tubing) |
|---|---|---|---|
| 1/4 wave | 46.3 cm / 18.2 in | 44.0 cm / 17.3 in | 44.5 cm |
| 1/2 wave | 92.6 cm / 36.5 in | 88.0 cm / 34.6 in | 88.9 cm |
| 5/8 wave | 115.7 cm / 45.6 in | 109.9 cm / 43.3 in | 111.1 cm |

Sanity check from a shipping product: the RTL-SDR Blog dipole kit's own tuning chart says "Large Antenna, 2 Sections, 42 cm + 2 cm is resonant @ ~162 MHz" — 44 cm, precisely the VF-corrected quarter wave per side ([RTL-SDR Blog dipole guide](https://www.rtl-sdr.com/using-our-new-dipole-antenna-kit/)). Wegmatt's manual gives the same number for a bare wire monopole/dipole: "even a simple wire cut to length (~92 cm)" ([dAISy-catcher manual §10.2](https://wegmatt.com/files/dAISy-catcher%20AIS%20Receiver%20Manual.pdf)).

---

## 2. Propagation: why height is the whole game

AIS is line of sight. The signal does bend a little with atmospheric refraction — the standard correction is to pretend the Earth is 4/3 its actual radius — but it does not go over hills.

**Radio horizon:**

    d (nm) = 1.23 × √(h in feet)
    d (km) = 4.12 × √(h in metres)

The Arun Dale AIS site derives it explicitly as `dr = √((4587 + h/6076)² − 4587²)` with 4587 nm being the 4/3-Earth radius ([arundaleais horizon page](https://arundaleais.github.io/docs/ais/horizon.html)).

**The critical detail most guides omit:** your range to a ship is *your* horizon **plus the ship's** horizon. Both antennas are above the geometric horizon of the tangent point. This is why a masthead antenna at 10 m still hears container ships at 16+ nm: the ship contributes 8–9 nm of its own.

| Your antenna height | Your horizon | + ship @ 15 m (8.6 nm) | + ship @ 30 m (12.2 nm) |
|---|---|---|---|
| 2 m (pushpit rail) | 3.2 nm / 5.8 km | 11.8 nm | 15.4 nm |
| 10 m (sailboat masthead, house roof) | 7.0 nm / 13.0 km | 15.6 nm | 19.2 nm |
| 20 m (small tower) | 9.9 nm / 18.3 km | 18.5 nm | 22.1 nm |
| 30 m (tall building) | 12.2 nm / 22.6 km | 20.8 nm | 24.4 nm |
| 100 m (hilltop) | 22.3 nm / 41.2 km | 30.9 nm | 34.5 nm |
| 300 m (real hill) | 38.6 nm / 71.4 km | 47.2 nm | 50.8 nm |
| 700 m (island mountain) | 59.0 nm / 109 km | 67.6 nm | 71.2 nm |

MarineTraffic's own published figures line up: 15 m → 15–20 nm, 20 m → ~25 nm, elevated base stations → 40–60 nm, and their network team tracked vessels at **200 nm using a small portable antenna on an island mountain at 700 m** ([MarineTraffic: typical range of an AIS station](https://support.marinetraffic.com/en/articles/9718923-what-is-the-typical-range-of-an-ais-station)).

**Why height beats gain.** Doubling height buys √2 ≈ 41% more horizon — but it is the only thing that moves the horizon at all. Gain does not extend line of sight; it only concentrates the pattern you already have. A ship 5 nm past your geometric horizon is not "weak", it is *behind the Earth*, and a 10 dB antenna will not find it. Every primary source in this space says the same thing in different words:

- Wegmatt: "The most important factor is the elevation of the antenna… In a nutshell: The farther you can see, the better." ([dAISy-catcher manual §10.1](https://wegmatt.com/files/dAISy-catcher%20AIS%20Receiver%20Manual.pdf))
- MarineTraffic: altitude is "the most crucial factor" ([support article](https://support.marinetraffic.com/en/articles/9718923-what-is-the-typical-range-of-an-ais-station))
- RTL-SDR Blog: "Antenna height is important, the higher and more unobstructed the better." ([AIS tutorial](https://www.rtl-sdr.com/rtl-sdr-tutorial-cheap-ais-ship-tracking/))
- AIS-catcher community: "Your antenna is the most important piece of equipment that affects the number of ships you will receive." ([Discussion #333](https://github.com/jvde-github/AIS-catcher/discussions/333))

Terrain trumps both. Wegmatt again: "A few buildings and trees between you and your targets aren't ideal, but you will still be able to receive many messages. Hills and mountains, however, are almost certain showstoppers."

### Ducting: the outlier reports

Tropospheric ducting — a temperature inversion forming a waveguide over water — periodically produces absurd ranges. These are real, but they are weather, not equipment.

| Reported range | Conditions | Source |
|---|---|---|
| 140+ nm | Ducting event, otherwise 25 nm station | [AIS-catcher Discussion #378](https://github.com/jvde-github/AIS-catcher/discussions/378) |
| 150–180 nm | Masthead / dual-band ham antenna | [YBW AIS propagation thread](https://forums.ybw.com/threads/ais-propgation.506270/) |
| 200 nm | 700 m island mountain, small portable antenna | [MarineTraffic](https://support.marinetraffic.com/en/articles/9718923-what-is-the-typical-range-of-an-ais-station) |
| 283 nm | Explicitly attributed to ducting | [YBW thread](https://forums.ybw.com/threads/ais-propgation.506270/) |
| "a bit over 700 miles" (vs 70 mi winter baseline) | 900 ft elevation, summer ducting | [RadioReference AIS recommendations](https://forums.radioreference.com/threads/ais-recommendations.354745/) |

Do not size your expectations off these. Size them off the horizon table, then enjoy the summer surprises.

---

## 3. Antennas, cheapest to best

### Tier 0 — the dipole already in the box ($0)

If you own an RTL-SDR Blog V3/V4 kit, extend **both** telescopic elements of the large antenna to **42 cm of telescopic length (44 cm total including the 2 cm inside the base)** and mount it vertically ([RTL-SDR Blog dipole guide](https://www.rtl-sdr.com/using-our-new-dipole-antenna-kit/)). The dipole kit alone is $17.95; the V4-plus-dipole bundle is $59.95 ([RTL-SDR Blog store](https://www.rtl-sdr.com/store/)).

This is a **verification** antenna, not a station antenna. Get it in a window or on a balcony, confirm you decode something, then plan the real install. Indoors at ground level you will see a few miles.

### Tier 1 — DIY ($0–20, plus a weekend)

A quarter-wave ground plane is the highest value-per-dollar object in this entire guide.

- Vertical element: **44 cm** (17.3 in)
- Four radials, sloped down 30–45°, cut ~5% long: **46–48 cm** (18–19 in)
- Feed at the base, centre conductor to the vertical, braid to the radials
- Calculator: [M0UKD quarter-wave ground plane](https://m0ukd.com/calculators/quarter-wave-ground-plane-antenna-calculator/)

An AIS-catcher station operator reports: "I built a homebuilt quarter-wave ground plane antenna… it works very well and cost almost nothing to build… a good choice if you like DIY," tuned with a NanoVNA ([Discussion #333](https://github.com/jvde-github/AIS-catcher/discussions/333)).

A **Slim Jim / J-pole** in copper pipe or 300 Ω twin lead is the other classic. Scaled to 162 MHz via the [M0UKD calculator](https://m0ukd.com/calculators/slim-jim-and-j-pole-calculator/):

| Slim Jim dimension | 162 MHz |
|---|---|
| Half-wave radiator | 88.9 cm |
| Quarter-wave matching stub | 44.5 cm |
| Feedpoint tap above stub base | ~8.9 cm |
| Gap between radiator and stub | ~1.8 cm |

RTL-SDR Blog specifically calls out slim-jims as working well for AIS and buildable "on a budget using common 300 Ohm twin lead ribbon cable" ([AIS tutorial](https://www.rtl-sdr.com/rtl-sdr-tutorial-cheap-ais-ship-tracking/)).

**Coax collinear — the best-documented DIY AIS antenna anywhere.** The Arun Dale AIS site publishes two builds with measured before/after ranges ([arundaleais aerial page](https://arundaleais.github.io/docs/ais/aerial.html)):

| Build | Elements | Cut lengths (RG-58, VF 0.66) | Measured range |
|---|---|---|---|
| Plain wire (baseline) | — | — | ~10 miles |
| Generic marine VHF whip | — | — | slightly better than wire |
| Diamond F-22 (144 MHz ham vertical) | — | — | 20–25 miles |
| **Mk1 collinear (~6 dB)** | 4 coax sections + top rod | 63.9 cm cut per section (61.1 cm active braid); top rod 46.3 cm | **25–30 miles** |
| **Mk2 collinear (~9 dB)** | 8 coax sections + top rod + ground plane | same per section; ground-plane tube 46.3 cm | **35–45 miles** |

The author notes the ground plane alone "appears to increase the range by about 20%." Fed with RG-213. That table is the single most useful empirical antenna comparison in the AIS hobby — note that a *purpose-built collinear roughly doubles the range of a generic marine whip at the same height*, and that a ham 2 m antenna badly detuned from 162 MHz still beat a random wire.

### Tier 2 — a real marine whip ($50–120). **This is the recommendation for most people.**

Wegmatt, who make dedicated AIS hardware and have no antenna to sell you, put it plainly: "any antenna sold as 'marine VHF antenna' will be a good start… A step up is the VHF whip antenna. These are steel rods about 90 centimeters (3ft) long. While bulky, these don't cost much more than the 'rubber duck' but provide superior range. Two inexpensive options available in the US are **TRAM 1600-HC** and **Shakespeare 5215** which cost US $50-75." ([dAISy-catcher manual §10.2](https://wegmatt.com/files/dAISy-catcher%20AIS%20Receiver%20Manual.pdf))

They also warn against the obvious cheap shortcut: "the more broadband the antenna, the worse the reception quality." A discone or wideband scanner antenna will receive AIS, but it hands your dongle the entire spectrum at once.

| Antenna | Length | Gain | Price | Notes |
|---|---|---|---|---|
| [Tram 1600-HC](https://www.amazon.com/Tram-3dBd-Gain-Marine-Antenna/dp/B00TY2KPT6) | 39 in whip | 3 dBd | ~$40–50 | 156–162.2 MHz, ships with 23 ft RG-58. Wegmatt's personal pick. |
| [Shakespeare 5215](https://www.westmarine.com/shakespeare-5215-3--classic-vhf-squatty-body-antenna-251442.html) "Squatty Body" | 3 ft | 3 dB | ~$86–100 | The generic marine standard |
| [Shakespeare 5215-AIS](https://www.fisheriessupply.com/shakespeare-squatty-body-36in/5215-ais) | 3 ft | 3 dB, tuned 161.975–162.025, VSWR <1.5:1 | ~$86–100 | Real distinct SKU, narrowed to the AIS pair |
| [Shakespeare 5250-AIS](https://www.fisheriessupply.com/shakespeare-skinny-mini-36in/5250-ais) "Skinny Mini" | 3 ft | 3 dB, AIS-tuned | ~$85–100 | Slimmer profile |
| [Glomex RA300AIS](https://www.svb24.com/en/glomex-ais-antenna-with-fiberglass-tube-ra300-fme-1-2-m.html) | 1.2 m | 3 dB, 161.975–162.025, SWR ≤1.3 | ~€48.70 | Cheapest purpose-built AIS antenna found |
| [Digital Yacht KS30](https://digitalyacht.co.uk/product/ks30-vhf-antenna/) | 1 m | AIS-tuned 162 MHz | £85 | Ships with 5 m RG-58 + BNC |
| [Metz AIS whip](https://www.bluemarinestore.com/metz-ais-stainless-steel-vhf-whip-antenna/) | stainless whip | AIS-tuned | €139.95 | Metz Manta-6 (general marine) is €158.99 |

**Is the "-AIS" suffix worth paying for?** It is not marketing, but it is a small effect. Shakespeare tunes its standard marine antennas for **VSWR <1.5:1 at 156.8 MHz** — the middle of the voice band — with only **3 MHz of bandwidth within 2.0:1 VSWR** on the Galaxy series ([Shakespeare Galaxy page](https://shakespeare-marine.com/product/galaxy-vhf-antenna/)). 162.025 MHz is 5.2 MHz above centre, i.e. *outside* that spec window. The Classic 5215 is more forgiving at 7 MHz bandwidth ([Shakespeare Classic page](https://shakespeare-marine.com/product/classic-vhf-antenna/)). So on a high-gain 8 ft Galaxy the AIS variant genuinely matters; on a 3 ft 3 dB whip it barely does.

Shakespeare lists the -AIS parts as "matching antennas" — they are sold as the *second* antenna you add alongside your voice VHF whip, not as replacements.

**Watch out:** the 5225-XT-AIS is specified as **DC ground shorted** ([Shakespeare Galaxy page](https://shakespeare-marine.com/product/galaxy-vhf-antenna/)). That is good for static and lightning, and fatal if you switch on your dongle's bias tee with the antenna connected directly. See §5.

It is worth *nothing* compared to putting the antenna 5 m higher. If the tuned and untuned versions are within $20, take the tuned one; if the gap is large, take the cheaper one and spend the difference on mast.

### Tier 3 — high gain, fixed installations only ($150–1150)

| Antenna | Gain | Price | Source |
|---|---|---|---|
| [Shakespeare 5225-XT-AIS](https://www.fisheriessupply.com/shakespeare-5225-xt-ais-galaxy-ais-antenna-8-ft-6-db/5225-xt-ais) "Galaxy", 8 ft | 6 dB | ~$200–260 | |
| [Morad VHF-162 HD-AIS](https://www.morad.com/products/vhf-antenna-162-hd-ais) | 6 dB @ 162 MHz | $263 (white) / $273 (black) | Purpose-built AIS, US-made |
| [Morad VHF-162 10dB-AIS](https://www.morad.com/products/vhf-antenna-162-10db-ais-ups) | 10 dB @ 162 MHz | $1,150 | 4-piece field-assembled |
| DIY Mk2 coax collinear | ~9 dB | ~$20 | [arundaleais](https://arundaleais.github.io/docs/ais/aerial.html) |

**The gain/pattern trade-off, and why it matters differently on a boat.** Antenna gain at VHF is not free energy; it is the vertical pattern squeezed flatter toward the horizon. Every 3 dB doubles effective radiated power in the main lobe while narrowing the beam ([Practical Sailor: Antenna Gain and VHF Transmission Range](https://www.practical-sailor.com/marine-electronics/antenna-gain-and-vhf-transmission-range/)).

| Gain | Typical length | Vertical pattern | Use on |
|---|---|---|---|
| 3 dB | 3–4 ft | Wide, forgiving | Sailboats (heel), any first install |
| 6 dB | 8 ft | Narrower oval | Powerboats, fixed shore masts |
| 9–10 dB | 16 ft+ | Pancake / "biscuit" | Fixed shore mast or hilltop **only** |

On a heeling sailboat a 9 dB antenna's lobe points into the water on one tack and the sky on the other. On a fixed shore mast that never happens and you bank the whole gain.

But there is a second, less obvious trap that applies even to fixed stations. The Arun Dale author, who built the 9 dB collinear, warns that a high-gain collinear's narrow vertical beam is "a 'biscuit'" and can put nearby low-antenna targets "below the radar" — a small boat 1 nm away at 2 m antenna height sits at a significant depression angle and falls out of the main lobe ([arundaleais](https://arundaleais.github.io/docs/ais/horizon.html)). If your value to the network is catching small local craft in a harbour, 3 dB may genuinely outperform 9 dB. If you sit on a hill watching a distant shipping lane, take the gain.

### Ham antennas and discones at 162 MHz

Dual-band ham verticals (Diamond X30A/X50A, Comet GP-3) are designed for 144–148 and 430–450 MHz. 162 MHz is ~10% above the 2 m band — outside the rated match, but not catastrophically so, and these are physically large well-built antennas. There is no published manufacturer SWR data at 162 MHz. Empirically they work: a Wegmatt customer reported **45–50 nm on a Diamond X30** ([dAISy HAT product page](https://shop.wegmatt.com/products/daisy-hat-ais-receiver)), and the Arun Dale comparison measured a Diamond F-22 at 20–25 miles, beating a generic marine whip. Verdict: **fine if you already own one; not worth buying new** when a purpose-built marine whip costs less and is actually resonant.

Discones (Diamond D130, Tram 1410, Sirio SD-series) are roughly 0 dBi across a huge bandwidth. They will decode AIS, but they present every strong signal from 25 MHz to 1.3 GHz to your dongle's front end simultaneously, which is exactly the condition that causes the overload problems in §5. No AIS operator in the sources consulted chose a discone deliberately for AIS. Skip.

---

## 4. Coax and connectors

### Loss at 162 MHz

Manufacturers publish attenuation at fixed test frequencies (50 / 100 / 150 / 200 / 220 / 400 MHz), not at 162 MHz. Values below are interpolated from the bracketing datasheet points using the standard α(f) ≈ a√f + bf model, which is accurate to well under a tenth of a dB over this small a span.

| Cable | dB/100 ft @ 162 MHz | dB/100 m | OD | VF | ~$/ft | Notes |
|---|---|---|---|---|---|---|
| RG-174 | 11.1 | 36.3 | 2.79 mm | 0.66 | $0.15–0.25 | Jumpers only. Never a feedline. |
| RG-58 | 5.0 | 16.3 | 4.9 mm | 0.66 | $0.25–0.45 | The default cheap cable; fine short |
| LMR-195 | 4.6 | 15.1 | 4.95 mm | 0.85 | $0.60–0.90 | RG-58 sized, better shield |
| RG-8X | 4.0 | 13.2 | 6.15 mm | ~0.78 | $0.40–0.70 | Ships with most marine antennas |
| Messi & Paoloni Hyperflex 5 | 3.1 | 10.2 | 3.7 mm | 0.87 | ~$1.50–2.00 | Very flexible for the loss |
| LMR-240 | 3.1 | 10.3 | 6.1 mm | 0.84 | $0.90–1.30 | **Sweet spot for ≤15 m runs** |
| RG-213 | 2.8 | 9.1 | 10.29 mm | 0.66 | $0.60–1.00 | Thick, cheap, stiff |
| M&P Ultraflex 7 | 2.2 | 7.3 | 7.3 mm | 0.83 | ~$1.85 | Best flexibility per dB |
| LMR-400 | 1.6 | 5.2 | 10.29 mm | 0.85 | $1.45–2.80 | The standard for long runs |
| LMR-600 | 1.0 | 3.4 | 14.99 mm | ~0.87 | $2.50–4.00 | Overkill below ~50 m |

Datasheet anchor points, straight from the PDFs: LMR-400 = **1.5 dB/100 ft (5.0 dB/100 m) at 150 MHz**, LMR-240 = **3.0 dB/100 ft at 150 MHz** ([Times Microwave LMR-400](https://timesmicrowave.com/DataSheets/CableProducts/LMR-400.pdf), [LMR-240](https://timesmicrowave.com/DataSheets/CableProducts/LMR-240.pdf)); RG-8X = 3.1 dB/100 ft @ 100 MHz, 4.5 @ 200 MHz ([Belden 9258](https://catalog.belden.com/techdata/EN/9258_techdata.pdf)); RG-58 = 3.8 @ 100 MHz, 5.6 @ 200 MHz ([Belden 8240](https://catalog.belden.com/techdata/EN/8240_techdata.pdf)); RG-213 = 2.1 @ 100 MHz ([Pasternack RG213-U](https://www.pasternack.com/images/ProductPDF/RG213-U.pdf)); [M&P Ultraflex 7](https://messi.it/dati/layout/files/CartellaElementi/Ultraflex%207%20-%20Full%20Datasheet%20ENG.pdf) = 6.9 dB/100 m @ 144 MHz.

### What loss actually costs you

| 10 m run | Loss | Cable cost |
|---|---|---|
| RG-58 | **1.63 dB** | $8–15 |
| RG-8X | 1.32 dB | $13–23 |
| LMR-240 | 1.02 dB | $30–43 |
| LMR-400 | **0.52 dB** | $48–92 |

Upgrading a 10 m run from RG-58 to LMR-400 costs ~$40–75 and recovers **1.1 dB**.

Is 1.1 dB worth $60? Do the conversion, because the answer is less obvious than it sounds. Loss ahead of the first amplifier adds to system noise figure **one dB for one dB** — a passive attenuator's noise figure equals its loss, and being first in the chain, nothing downstream can recover it. In free space received power falls as 20·log₁₀(distance), so 1 dB of margin is worth 10^(1/20) = **12% of range**, and since coverage area scales with radius², **~26% of coverage area**.

So yes: on a receive-only station whose entire product is coverage, 1.1 dB of cable is roughly a quarter of your coverage area for $60. That said — the same 1.1 dB is worth about 1.7 nm at a 15 nm station, while raising the antenna from 10 m to 20 m adds 2.9 nm for the cost of a pole. **Spend on height first, cable second.**

### The three rules

1. **Keep the run short.** Every metre is loss you can never get back. A 30 m run of RG-58 is 4.9 dB — you have thrown away nearly half your range before the signal reaches the dongle.
2. **If it must be long, put an LNA at the antenna** so the loss lands after the gain stage instead of before it (§5).
3. **If it must be very long, don't run coax at all** — run Ethernet (§4, below).

MarineTraffic's threshold for a station install: RG-58 is acceptable to 15–20 m; beyond that use RG-213, Aircell 5 or similar. And critically: *"It is also better to use a longer network cable than to bridge longer distances with the Coax cable."* ([MarineTraffic install guide](https://support.marinetraffic.com/en/articles/9552957-how-to-install-an-ais-station))

### Connectors

| Where | Connector |
|---|---|
| RTL-SDR Blog V3 / V4, Airspy, dAISy-catcher | **SMA female** |
| Nooelec NESDR Mini 2 and older Nooelec dongles | **MCX female** |
| Marine VHF/AIS antennas (Shakespeare, Glomex, Digital Yacht, Comar) | **SO-239 (UHF female)**, mating with PL-259 |
| Active splitter AIS output ports | usually **BNC** |
| Uputronics / GPIO Labs filters and LNAs | **SMA female** both ends |

So the typical chain is: marine antenna (SO-239) → coax with PL-259 → **PL-259/SO-239-to-SMA-male adapter** → dongle. That adapter is the one part everyone forgets to order.

| Part | Price |
|---|---|
| UHF-to-SMA adapter (Pasternack PE9M series) | $4–8 |
| Generic SMA ↔ PL-259/SO-239 adapter | $6–12 |
| MCX-to-SMA pigtail | $5–12 |
| [RTL-SDR Blog SMA pigtail adapter set](https://www.rtl-sdr.com/store/) | $18.95 |
| 23-in-1 adapter kit | $20–30 |

Budget **0.1–0.3 dB per junction** and don't stack three adapters where one custom pigtail would do — each one is also a water ingress path.

**Are UHF connectors acceptable at 162 MHz?** Yes, completely, and the common warning does not apply here. The SO-239's flaw is a geometry discontinuity presenting roughly 35 Ω against a 50 Ω line ([Wikipedia: UHF connector](https://en.wikipedia.org/wiki/UHF_connector)). The resulting loss scales with frequency because the discontinuity becomes a larger fraction of a wavelength: about 1.0 dB measured at 432 MHz, negligible at or below ~150–300 MHz. At 162 MHz you are looking at a small fraction of a dB. This is exactly why every marine VHF antenna manufacturer standardises on PL-259/SO-239 for this band, and why an N-type conversion is not worth the trouble on an AIS station. (If you are already inside the box with SMA, stay with SMA.)

### Put the receiver at the antenna

The best answer to coax loss is to not have any. A Raspberry Pi and an RTL-SDR in a weatherproof box at the antenna, with a 30 cm jumper to the whip and Ethernet running down, has **zero feedline loss at any distance** and costs less than 30 m of LMR-400.

MarineTraffic endorses the principle directly ("better to use a longer network cable than to bridge longer distances with the Coax cable"), and it is standard practice in the closely related ADS-B feeder community, where the same physics applies.

Practical notes:

- **Power over Ethernet** is the clean version: an official Raspberry Pi PoE+ HAT (802.3af/at) or a generic PoE-to-USB-C splitter for a Pi Zero class board. One cable does power and data.
- **Enclosure:** IP65/66 box with a cable gland for the Ethernet and a bulkhead SMA or N connector for the antenna, so the RF joint is on the outside of the box and sealed.
- **Heat is the real failure mode.** A sealed box in direct summer sun will thermally throttle a Pi 4 and, worse, drift your dongle's oscillator — and RTL-SDR frequency drift is the single most-documented cause of AIS decode failure. AIS-catcher's own troubleshooting page leads with PPM correction and recommends `-go AFC_WIDE on` for "dongles experiencing thermal drift" ([AIS-catcher troubleshooting](https://jvde-github.github.io/AIS-catcher-docs/advanced/troubleshooting/)). Shade the enclosure, use a light colour, mount it with an air gap, and prefer a TCXO dongle if you go this route.
- Sacrificing a bit of temperature stability to eliminate 4 dB of cable is still usually the right trade — but budget for the heat.

---

## 5. Filters and LNAs

### The mental model

An RTL-SDR has an 8-bit ADC and roughly 45–50 dB of usable instantaneous dynamic range. It does not care that you are tuned to 162 MHz — the front end sees *everything* the antenna delivers. A 100 kW FM broadcast transmitter 5 km away, a pager transmitter on 152 MHz, a marine voice repeater on 157 MHz, or an LTE tower will drive the tuner's AGC down and the mixer into compression, and your AIS sensitivity collapses even though nothing is transmitting on 162 MHz.

This produces the single most important rule in this section:

> **A filter helps by removing signal. An LNA helps by adding gain. If your problem is overload, the LNA makes it worse.**

The AIS-catcher community learned this the hard way. From [Discussion #333](https://github.com/jvde-github/AIS-catcher/discussions/333), on the plain RTL-SDR Blog wideband LNA: *"Your reception will almost certainly be worse if you are near a strong AIS base station."* Wegmatt's manual says the same about their receiver: *"in most scenarios external amplification will not improve reception. To the contrary, external amplification may overload the frontend of the AIS receiver, worsening reception."* ([dAISy-catcher manual §10.3](https://wegmatt.com/files/dAISy-catcher%20AIS%20Receiver%20Manual.pdf))

### When you actually need each

| Symptom | Fix |
|---|---|
| Noise floor rises and falls, message count swings wildly through the day | Bandpass filter |
| You live near an FM broadcast site, pager transmitter, or airport | Bandpass filter (or the notch filter, cheaper) |
| Long coax run (>20–30 m) and you cannot move the receiver | LNA **at the antenna**, ahead of the coax |
| Rural, quiet band, weak distant targets, short coax | LNA may help slightly |
| Urban, dense RF, already decoding fine | Change nothing |

Wegmatt's threshold is explicit: external amplification "may be beneficial with a very long antenna cable (>30 meters). In this scenario, the external amplifier must be installed near the antenna."

### The products

| Product | Type | Gain | NF | Passband / rejection | Insertion loss | Power | Price |
|---|---|---|---|---|---|---|---|
| [GPIO Labs AIS Bandpass Filter 160–162 MHz](https://gpio.com/products/ais-bandpass-filter-160-162-mhz) | Passive BPF | — | — | 68 dB @ 88 MHz, 50 dB @ 108 MHz, 30 dB @ 250 MHz, 42 dB @ 500 MHz; +30 dBm max in | **2.8 dB** | none | **$33** (SMA-F), $42 in enclosure |
| [Wegmatt TB0436A narrowband SAW](https://shop.wegmatt.com/products/tb0436a-narrowband-saw-filter-for-ais) | Passive SAW | — | — | 162 MHz centre, 90–108 kHz 3 dB BW, 50 dB BW 415–450 kHz | **3.0–6.0 dB** | none | **$29** |
| [Uputronics 162 MHz AIS Filtered Preamp](https://shop.wegmatt.com/products/uputronics-filtered-preamplifier-for-ais) | SAW BPF + LNA (PSA4-5043+) | ≥20–22 dB | **0.78 dB** | 159–163 MHz, 7.6 MHz BW; HPF ahead of the LNA to kill broadcast FM | ~1.4 dB (filter) | 5 V bias-tee or USB-C, 50 mA | **$59** (Wegmatt) / $59.95 ([Airspy.US](https://v3.airspy.us/product/upu-fp162s/)) / £44.39 ([Uputronics](https://store.uputronics.com/products/uputronics-filtered-preamps)) |
| [GPIO Labs AIS Filtered LNA](https://gpio.com/products/ais-filtered-low-noise-amplifier) | BPF + LNA | 20 dB (160–164 MHz) | <1 dB | FM/cell/LTE/UHF/915/2.4 G rejection; +15 dBm max in, OIP3 +28 dBm | — | 3.3–16 V, micro-USB or 2-pin | **$84** |
| [RTL-SDR Blog FM band-stop filter](https://www.rtl-sdr.com/store/) | 88–108 MHz notch | — | — | 7th-order Chebyshev | low | none | **$16.95** |
| [RTL-SDR Blog Wideband LNA](https://www.rtl-sdr.com/product/rtl-sdr-blog-wideband-lna-bias-tee-powered/) | SPF5189Z, 50 MHz–4 GHz | ~20 dB | <1 dB | **none — unfiltered** | — | bias tee | $19.95 |

**Products that do not exist, so you can stop looking:** Nooelec makes SAWbird modules for NOAA (137 MHz), ADS-B (1090/978 MHz), and L-band (1542 MHz), but **there is no SAWbird+ AIS** — a search of the Nooelec store for "AIS" returns only general-purpose NESDR dongles ([nooelec.com](https://www.nooelec.com/store/)). RTL-SDR Blog likewise sells **no AIS-specific filter or LNA**; their catalogue has only the broadcast-FM band-stop, the broadcast-AM high-pass, and the unfiltered wideband LNA ([RTL-SDR Blog store](https://www.rtl-sdr.com/store/)).

Generic "AIS SAW filter" modules on AliExpress and eBay are typically the same TA0436A/TB0436A-class SAW part as Wegmatt's, in a cheaper package, without published insertion-loss data. Given the Wegmatt part is $29 from a vendor who documents 3–6 dB insertion loss, the savings are not compelling.

### Does any of it actually help? The honest evidence

The community reports are genuinely mixed, and that is the finding.

**Filtering helps in noisy environments, measurably:**
- Side-by-side on the same antenna: "regular RTL dongle no filter 62 messages / min, ShipExplorer AIS dongle [SAW-filtered] 74 messages / min" — a ~19% improvement ([AIS-catcher Discussion #244](https://github.com/jvde-github/AIS-catcher/discussions/244)). The maintainer noted the filtered dongle was "cheaper than buying a V3 + AIS filter."
- The Wegmatt SAW filter "improves the number of messages received for most AIS receivers, especially in urban areas, or high traffic areas" ([product page](https://shop.wegmatt.com/products/tb0436a-narrowband-saw-filter-for-ais)).
- A station suffering "wild changes in reception" traced to external RF interference resolved it with a Uputronics filtered preamp plus a GPIO Labs filter plus a move from RTL-SDR V3 to an Airspy R2 "for better broadband rejection," reaching "25+ miles" routinely and 140 nm during ducting ([Discussion #378](https://github.com/jvde-github/AIS-catcher/discussions/378)).

**And does nothing in quiet environments:**
- "I have two of these [Uputronics AIS SAW filtered preamps] and don't see any improvement in reception when used with a RTL-SDR V3." ([Discussion #333](https://github.com/jvde-github/AIS-catcher/discussions/333))
- The same comparison found the GPIO Labs passive filter "reduced interference spikes but unclear overall benefit."
- Note the filtered dongle comparison above showed *decode quality in noise*, not range: complaints on that product centre on "unmet expectations about range improvements."

**Practical conclusion.** Buy the antenna and the coax first. Only buy a filter after you have looked at your own noise floor — AIS-catcher's web viewer plots signal level and message rate over time, and an interference problem shows up as a rising noise floor with dips in message count, exactly as diagnosed in Discussion #378. If you do buy, a **$33 passive bandpass filter is the right first purchase** (it can only help if overload is your problem, and costs you 2.8 dB if it isn't). Reach for the **$59 Uputronics filtered preamp** only when you also have coax loss to make up.

### Where to put things

Noise figure cascades. The first component sets the floor, and every dB of loss ahead of the amplifier adds a dB to system NF one-for-one. So:

    Antenna → (short jumper) → filtered LNA → long coax → dongle

Not:

    Antenna → long coax → LNA → dongle    ← the coax loss is already baked in

If you cannot mount the LNA outdoors, that is a strong argument for moving the whole receiver to the antenna instead (§4).

A passive filter placed *before* the LNA protects the LNA from overload but costs you its insertion loss directly in NF. Placed *after* the LNA it does not protect anything. Integrated products like the Uputronics resolve this by putting a high-pass ahead of the LNA (to dump broadcast FM, the usual culprit, at near-zero NF cost) and the sharp SAW filter after it.

### Bias-tee powering

Both the RTL-SDR Blog V3 and V4 have a software-switchable bias tee: **4.5 V, up to 180 mA continuous**, enabled with `rtl_biast -b 1` (and off with `-b 0`) ([RTL-SDR Blog V3 user guide](https://www.rtl-sdr.com/rtl-sdr-blog-v-3-dongles-user-guide/)). The V4 adds an LED so you can see it is on ([V4 release post](https://www.rtl-sdr.com/rtl-sdr-blog-v4-dongle-initial-release/)). That is comfortably enough for a 50 mA Uputronics preamp.

In AIS-catcher, set it in the device config — the working example from Discussion #378 is:

```json
{"tuner":"6", "bandwidth":"0", "sample_rate":"1536K",
 "biastee":true, "rtlagc":false, "freqoffset":0}
```

In [docker-shipfeeder](https://github.com/sdr-enthusiasts/docker-shipfeeder) it is `RTLSDR_DEVICE_BIASTEE=on`, alongside `RTLSDR_DEVICE_GAIN` (default 33 for RTL-SDR), `RTLSDR_DEVICE_RTLAGC` (default on), and `RTLSDR_DEVICE_BANDWIDTH` (default 192K).

**The one way to destroy your dongle:** RTL-SDR Blog warns "Do not use this option when the dongle is connected directly to a DC short circuited antenna unless you are using an LNA." Many antennas — including most marine whips and any DC-grounded design — are a dead short at DC. Turning the bias tee on into one relies on a resettable fuse to save you. Correct order is always `DC-shorted antenna → LNA → coax → dongle (bias tee on)`.

Note also that **DC-blocked lightning arrestors will kill bias-tee power** to your LNA. If you are powering an LNA up the coax, you need a gas-discharge-tube arrestor that passes DC, not a DC-blocked one.

### A note on the dongle itself

The V4 is not automatically better than the V3 for AIS. Its triplexed front end gives 28–43 dB better out-of-band isolation but costs "an average of 2-3 dB less sensitivity on some bands" ([V4 release post](https://www.rtl-sdr.com/rtl-sdr-blog-v4-dongle-initial-release/)). One AIS operator measured exactly that: "the V4 unit I have gives worse results than the V3 dongle… it's supposed to be better" ([Discussion #333](https://github.com/jvde-github/AIS-catcher/discussions/333)). In a *quiet* location the V3 may win; in a noisy one the V4's filtering is the point. This is the same filter-versus-sensitivity trade-off as everything else in this section, just moved inside the dongle.

The purpose-built alternative is the [Wegmatt dAISy-catcher](https://shop.wegmatt.com/products/daisy-catcher-high-performance-ais-receiver), $149, co-developed with the AIS-catcher author and recommended in the AIS-catcher README. Sensitivity better than −120 dBm @ 20% PER, integrated 23 dB LNA and a SAW filter with a 156.3–162.025 MHz passband, SMA female, 50 Ω, 500 mW. It has a hardware step attenuator that engages above −42 dBm and an LED that tells you when it is being overloaded — a genuinely useful diagnostic that no RTL-SDR gives you. Absolute maximum input **0 dBm**; recommended maximum **−40 dBm**.

---

## 6. Lightning, grounding, mounting, weatherproofing

Brief, because none of it improves reception — it just stops you rebuilding the station.

### Siting and lightning

MarineTraffic's own installation guidance is the most quotable primary source here ([how to install an AIS station](https://support.marinetraffic.com/en/articles/9552957-how-to-install-an-ais-station)):

- Mount outside, high, with a clear view of the water, **away from power lines**
- **Do not make your antenna the highest point in the area**; integrate it below an existing lightning rod / air terminal and follow local electrical code
- Keep the receiver indoors, dry, cool, out of direct sun

The practical hierarchy: a properly bonded lightning protection system if the structure has one, then a coax surge arrestor at the point the cable enters the building, bonded to the same ground electrode as the building's electrical service. Two grounds at different potentials is worse than one.

**Surge arrestor type matters for your LNA.** Coax arrestors come in two flavours:

| Type | How it works | Passes DC? | Use when |
|---|---|---|---|
| Gas discharge tube (GDT) | Spark gap fires above a threshold voltage | **Yes** | You are feeding bias-tee power up the coax to an LNA |
| DC block / quarter-wave stub | Shorts DC and out-of-band energy to ground | **No** | Passive antenna, no LNA |

If you install a DC-blocked arrestor in a line carrying bias-tee power, your LNA simply never powers on and you will spend an afternoon confused. A typical GDT arrestor is the Alpha Delta ATT3G50, ~$68 (frequently out of stock; PolyPhaser and Times Microwave make equivalents at similar or higher prices). Note that alphadeltacom.com no longer belongs to the manufacturer — buy through DX Engineering, GigaParts or similar.

The cheapest and most effective lightning protection remains **unplugging the coax from the dongle and setting it outside the window before a storm**. For a hobby station with no tower, that is a reasonable policy.

### Weatherproofing

- Seal every outdoor connector with **self-amalgamating (self-fusing) tape**, not electrical tape — MarineTraffic calls this out explicitly. Electrical tape unwraps into a gummy mess in about one season of UV. Wrap: a layer of vinyl tape (so you can get it apart later), then self-amalgamating tape stretched to about half width with 50% overlap, then a UV-resistant vinyl outer layer.
- **Drip loop** below every connector so water runs off rather than into it.
- **Black, UV-resistant cable ties** only. Natural nylon ties turn to dust outdoors in a year or two.
- Point connectors **downward** where you can.
- LMR-style cables with a UV-resistant polyethylene jacket are rated for 20-year outdoor service ([Times Microwave LMR-240 datasheet](https://timesmicrowave.com/DataSheets/CableProducts/LMR-240.pdf)); indoor-rated PVC jackets are not.

### Mounting

- Mast, handrail, or direct to a wall using U-bolts or screws; MarineTraffic's requirement is only that fasteners "are securely tightened and cannot come loose"
- Stand the antenna **off** metal structures and away from the mast — a quarter wave (46 cm) of clearance is a good target, more is better
- Avoid sharp bends in coax; keep bend radius above the cable's rated minimum (roughly 10× diameter for braided coax)
- Vertical polarization, element straight up

### Separation from other antennas

| Requirement | Distance | Source |
|---|---|---|
| From any other transmitting antenna | ≥ 1 m | [MarineTraffic](https://support.marinetraffic.com/en/articles/9552957-how-to-install-an-ais-station) |
| From high-power transmitters (radar, VHF voice) | ≥ 3 m, or out of the main beam | [Wegmatt dAISy-catcher manual §10.2](https://wegmatt.com/files/dAISy-catcher%20AIS%20Receiver%20Manual.pdf) |

Vertical separation beats horizontal separation for a given distance.

### Local RF noise sources

Worth listing because they are the most common cause of a mysteriously bad station, and none of them are fixed by better hardware. Wegmatt's list, from field experience: **RGB LED strings, DSL/VDSL over phone wiring, and Ethernet-over-powerline adapters** ([dAISy-catcher manual §10.4](https://wegmatt.com/files/dAISy-catcher%20AIS%20Receiver%20Manual.pdf)). Add solar inverters — an AIS-catcher operator traced reception dips directly to theirs ([Discussion #378](https://github.com/jvde-github/AIS-catcher/discussions/378)). This noise is broadband, so a bandpass filter does not remove it; distance does. Ferrites on power leads help. Get the antenna away from the house.

---

## 7. Sharing an existing marine VHF antenna

Relevant only on boats, and to anyone tempted to tee off a transmitting antenna ashore. The short version, from the people who build AIS receivers:

> **IMPORTANT: Do NOT directly connect the dAISy-catcher to the same antenna as your VHF radio. This will damage the AIS receiver!** To share an existing antenna with a VHF radio, use an ACTIVE splitter. Active splitters protect the AIS receiver by automatically disconnecting it when the VHF radio is transmitting.
> — [dAISy-catcher manual §10.2](https://wegmatt.com/files/dAISy-catcher%20AIS%20Receiver%20Manual.pdf)

### Why a passive splitter is not an option

A passive two-way splitter is a resistive or transformer network. Three things go wrong at once:

1. **Loss both ways.** An ideal 2-way split is −3 dB; real passive VHF splitters run −3.5 to −4 dB. You give up a third of your received power permanently, on every message, to solve a problem that only exists while someone is keying the mic.
2. **No transmit isolation.** A 25 W marine VHF transmitter is **+44 dBm**. Through a −3.5 dB splitter your receiver sees roughly **+40 dBm** — about ten watts. The dAISy-catcher's absolute maximum input is **0 dBm** and its recommended maximum is **−40 dBm** ([manual §11](https://wegmatt.com/files/dAISy-catcher%20AIS%20Receiver%20Manual.pdf)). An RTL-SDR is no tougher. That is roughly **10⁴ times the destruct limit** — the front-end LNA is gone on the first transmission, silently and permanently.
3. **It degrades the radio too.** Your safety-critical voice VHF now transmits 3.5 dB less power and receives 3.5 dB worse.

There is no configuration in which this is the right answer. An active splitter costs less than the SDR you would destroy.

### What an active splitter does

An active splitter is a powered box with an RF-sensing switch and a preamplifier. When the radio transmits, it disconnects (or heavily attenuates) the AIS port within microseconds; when idle, it amplifies to make up the split loss, so insertion loss on receive is near zero or even negative. Better ones fail safe — losing power passes the antenna straight through to the radio, so you never lose your VHF because a fuse blew.

| Splitter | Price | Loss on receive | TX protection | Power | AIS port | Status Aug 2026 |
|---|---|---|---|---|---|---|
| [Glomex RA201](https://www.glomex.it/prodotto/leisure-glomeasy-en/ais-en-2/ra201/?lang=en) | budget (~€100–150) | +15 dB preamp shared across two outputs | 100 W max, 315 mA fuse | 13.8 V (10–16 V), 25 mA | **BNC male pigtail on RG-58** | Current; the one AIS-receiver customers keep reporting success with |
| [Digital Yacht SPL1500](https://digitalyacht.co.uk/product/spl1500/) | £275 | "ZeroLoss" patented | Fail-safe: on power loss or fault the VHF stays connected straight to the antenna | 12/24 V | BNC (0.5 m BNC-BNC assembly supplied) | Current |
| [Digital Yacht SPL2000](https://digitalyacht.co.uk/product/spl2000/) | £315 | not published | Same fail-safe design; "special circuitry to ensure safe operation of the two transmitting devices" | 12/24 V | BNC; also has an AM/FM output on the power lead | Current |
| [Comar AS350](https://comarsystems.com/product-category/splitters/) | not published | — | — | — | — | Current. **The AST200 is gone** — Comar's splitter catalogue now lists only the AS350 |
| Vesper SP160 | — | — | — | — | — | **Discontinued.** vespermarine.com now 301-redirects to garmin.com following the Garmin acquisition; there is no SP160 product page |
| em-trak splitters | — | — | — | — | — | *Unverified* — em-trak's site is behind Cloudflare and could not be checked |

Both Digital Yacht units advertise compatibility "with all Class B transponders and receivers," so a receive-only station is an intended use case, not a hack.

The one repeatedly reported working by AIS receiver customers is the **Glomex RA201**, a three-way VHF / AIS / AM-FM splitter with a "built-in preamplifier: 15 dB shared on two inputs," 13.8 V (10–16 V) at 25 mA, 100 W max VHF power, protected by a 315 mA glass fuse. Antenna input is SO-239; the **AIS output is a BNC male pigtail on RG-58** ([Glomex RA201](https://www.glomex.it/prodotto/leisure-glomeasy-en/ais-en-2/ra201/?lang=en)). Wegmatt names it in both their HAT and dAISy-catcher manuals: "Several customers reported good results with the inexpensive Glomex RA201 VHF/AIS/Radio Splitter."

### Compatibility with an SDR

All of these are 50 Ω and the AIS port is a plain receive output, so an RTL-SDR connects fine — you just need the adapter (BNC or SO-239 to SMA). Two cautions:

- **Do not enable the dongle's bias tee** into a splitter's AIS port. The splitter has its own power feed and its output stage is not expecting 4.5 V pushed back into it.
- Splitters designed for AIS **transponders** may expect the AIS port to occasionally transmit. That is harmless for a receive-only station, but check that the AIS port is not DC-fed by the splitter itself before connecting an SDR.

### The alternative: just run a second antenna

For a shore station this is the obvious answer, and for a boat it is usually cheaper than a splitter. The requirement is separation:

- **Vertical separation is best**, or failing that opposite sides of the vessel ([Wegmatt](https://wegmatt.com/files/dAISy-catcher%20AIS%20Receiver%20Manual.pdf))
- **At least 3 m** from, or out of the transmitting beam of, high-power transmitters such as radar or other VHF installations ([Wegmatt](https://wegmatt.com/files/dAISy-catcher%20AIS%20Receiver%20Manual.pdf))
- **Minimum 1 m** between the AIS antenna and any other transmitting antenna ([MarineTraffic install guide](https://support.marinetraffic.com/en/articles/9552957-how-to-install-an-ais-station))

Note these two figures are for different things: MarineTraffic's 1 m is the shore-station minimum to avoid mutual detuning and desense; Wegmatt's 3 m is about not destroying the receiver with a radar or a 25 W VHF. Use 3 m if there is a transmitter involved. Even 3 m of free-space path loss at 162 MHz only buys ~26 dB, so a 25 W transmitter still puts roughly +18 dBm into a nearby antenna — vertical separation and getting out of the main beam is doing most of the work, and an in-line limiter or the AIS receiver's own attenuator does the rest.

**Verdict for a volunteer station:** a Digital Yacht splitter is £275–315. A Shakespeare 5215 plus coax and a mount is under $150 and gives you a dedicated, correctly-tuned, independently-sited antenna with no insertion loss, no shared failure mode, and no risk to the boat's safety radio. Split an antenna only when there is physically nowhere to put a second one. Ashore, there is essentially never a reason.

---

## 8. What to actually buy

### Tier 1 — "I want to see if this works" (~$0–20 over the dongle you own)

Stock RTL-SDR dipole at 44 cm per element, vertical, in the highest window you have. Or a homebrew quarter-wave ground plane from wire and an SO-239 chassis mount. No filter, no LNA, no long coax. Expect a few miles and a working decoder.

### Tier 2 — the sweet spot (~$100–160 all in). **Most volunteer stations should build exactly this.**

| Item | Choice | Cost |
|---|---|---|
| Antenna | Tram 1600-HC or Shakespeare 5215 / 5215-AIS, 3 ft, 3 dB | $40–100 |
| Coax | LMR-240 or RG-8X, **kept under 10 m** | ~$20–40 |
| Adapters | PL-259/SO-239 → SMA pigtail | ~$8 |
| Filter / LNA | none yet | $0 |
| Mounting | pole/chimney/rail mount, self-amalgamating tape | ~$25 |

Get it on the roof, as high as you can safely reach, with a clear view of the water. This will out-perform a $600 antenna sitting in an attic.

### Tier 3 — add the parts that fix a diagnosed problem (~$30–90 more)

Only after Tier 2 is up and you have looked at your noise floor and message-rate plots:

- Message rate swings, rising noise floor, urban location → **[GPIO Labs 160–162 MHz bandpass filter, $33](https://gpio.com/products/ais-bandpass-filter-160-162-mhz)** (2.8 dB insertion loss) or **[Wegmatt TB0436A SAW, $29](https://shop.wegmatt.com/products/tb0436a-narrowband-saw-filter-for-ais)** (3–6 dB, much sharper)
- Coax run you cannot shorten, >20 m → **[Uputronics 162 MHz filtered preamp, $59](https://shop.wegmatt.com/products/uputronics-filtered-preamplifier-for-ais)**, mounted **at the antenna**, bias-tee powered
- Both problems, and you want one box → **[GPIO Labs AIS Filtered LNA, $84](https://gpio.com/products/ais-filtered-low-noise-amplifier)**

### Tier 4 — a serious fixed station ($300–600)

6 dB commercial antenna ([Shakespeare 5225-XT-AIS](https://www.fisheriessupply.com/shakespeare-5225-xt-ais-galaxy-ais-antenna-8-ft-6-db/5225-xt-ais) or [Morad VHF-162 HD-AIS](https://www.morad.com/products/vhf-antenna-162-hd-ais)) or a homebrew 9 dB coax collinear, on a real mast, with a filtered preamp at the antenna, LMR-400 down, a proper surge arrestor, and either a [dAISy-catcher](https://shop.wegmatt.com/products/daisy-catcher-high-performance-ais-receiver) ($149) or an Airspy for their better strong-signal handling.

Beyond this the money is better spent on **elevation and siting** than on any component in this document.

### Things not to buy

- A **discone or wideband scanner antenna** — broadband is the problem, not the solution
- An **unfiltered wideband LNA** in an urban location — it amplifies your interference along with your signal, and the AIS-catcher community's report is that it makes things *worse* near strong VHF sources
- A **passive VHF splitter** to share the boat's radio antenna — see §7
- A **Nooelec SAWbird+ AIS** — it does not exist
- **50 m of RG-58** to reach a distant antenna — put the Pi at the antenna instead
