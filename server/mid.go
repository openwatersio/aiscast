package main

// Flag state from an MMSI. The first three digits of a ship's MMSI are its Maritime Identification
// Digits (MID), allocated per country by the ITU (Recommendation ITU-R M.585, table of MIDs). Other
// station kinds carry the MID elsewhere in the number. The table maps a MID to the ISO 3166-1 alpha-2
// code of the flag, with overseas territories under their own codes where ISO has one. Every entry was
// checked against the published allocation list on 2026-09-18 (Wikipedia's copy of the ITU table; the
// ITU page renders its list with scripts). Check again when a new allocation appears; unknown MIDs
// yield "". 306 covers Curaçao, Sint Maarten, and the Caribbean Netherlands together and maps to CW.

// flagOf returns the ISO 3166-1 alpha-2 flag for an MMSI, or "" when the MID is unknown or the MMSI is
// not a ship, coast, group, handheld, SAR aircraft, or craft-associated identity.
func flagOf(mmsi uint32) string {
	var mid uint32
	switch {
	case mmsi >= 200_000_000 && mmsi < 800_000_000: // ships: MIDxxxxxx
		mid = mmsi / 1_000_000
	case mmsi >= 800_000_000 && mmsi < 900_000_000: // handheld VHF: 8MIDxxxxx
		mid = mmsi / 100_000 % 1000
	case mmsi >= 980_000_000: // craft associated with a parent ship: 98MIDxxxx and 99MIDxxxx
		mid = mmsi / 10_000 % 1000
	case mmsi >= 111_000_000 && mmsi < 112_000_000: // SAR aircraft: 111MIDxxx
		mid = mmsi / 1000 % 1000
	case mmsi >= 10_000_000 && mmsi < 100_000_000: // group ship stations: 0MIDxxxxx
		mid = mmsi / 100_000
	case mmsi < 10_000_000: // coast stations: 00MIDxxxx
		mid = mmsi / 10_000
	default: // 970 AIS-SART, 972 MOB, 974 EPIRB, and the unallocated 9xx block
		return ""
	}
	return midFlag[mid]
}

var midFlag = map[uint32]string{
	// Europe
	201: "AL", 202: "AD", 203: "AT", 204: "PT", 205: "BE", 206: "BY", 207: "BG", 208: "VA", 209: "CY", 210: "CY",
	211: "DE", 212: "CY", 213: "GE", 214: "MD", 215: "MT", 216: "AM", 218: "DE", 219: "DK", 220: "DK",
	224: "ES", 225: "ES", 226: "FR", 227: "FR", 228: "FR", 229: "MT", 230: "FI", 231: "FO", 232: "GB", 233: "GB",
	234: "GB", 235: "GB", 236: "GI", 237: "GR", 238: "HR", 239: "GR", 240: "GR", 241: "GR", 242: "MA", 243: "HU",
	244: "NL", 245: "NL", 246: "NL", 247: "IT", 248: "MT", 249: "MT", 250: "IE", 251: "IS", 252: "LI", 253: "LU",
	254: "MC", 255: "PT", 256: "MT", 257: "NO", 258: "NO", 259: "NO", 261: "PL", 262: "ME", 263: "PT", 264: "RO",
	265: "SE", 266: "SE", 267: "SK", 268: "SM", 269: "CH", 270: "CZ", 271: "TR", 272: "UA", 273: "RU", 274: "MK",
	275: "LV", 276: "EE", 277: "LT", 278: "SI", 279: "RS",
	// North and Central America, the Caribbean
	301: "AI", 303: "US", 304: "AG", 305: "AG", 306: "CW", 307: "AW", 308: "BS", 309: "BS", 310: "BM", 311: "BS",
	312: "BZ", 314: "BB", 316: "CA", 319: "KY", 321: "CR", 323: "CU", 325: "DM", 327: "DO", 329: "GP", 330: "GD",
	331: "GL", 332: "GT", 334: "HN", 336: "HT", 338: "US", 339: "JM", 341: "KN", 343: "LC", 345: "MX", 347: "MQ",
	348: "MS", 350: "NI", 351: "PA", 352: "PA", 353: "PA", 354: "PA", 355: "PA", 356: "PA", 357: "PA", 358: "PR",
	359: "SV", 361: "PM", 362: "TT", 364: "TC", 366: "US", 367: "US", 368: "US", 369: "US", 370: "PA", 371: "PA",
	372: "PA", 373: "PA", 374: "PA", 375: "VC", 376: "VC", 377: "VC", 378: "VG", 379: "VI",
	// Asia
	401: "AF", 403: "SA", 405: "BD", 408: "BH", 410: "BT", 412: "CN", 413: "CN", 414: "CN", 416: "TW", 417: "LK",
	419: "IN", 422: "IR", 423: "AZ", 425: "IQ", 428: "IL", 431: "JP", 432: "JP", 434: "TM", 436: "KZ", 437: "UZ",
	438: "JO", 440: "KR", 441: "KR", 443: "PS", 445: "KP", 447: "KW", 450: "LB", 451: "KG", 453: "MO", 455: "MV",
	457: "MN", 459: "NP", 461: "OM", 463: "PK", 466: "QA", 468: "SY", 470: "AE", 471: "AE", 472: "TJ", 473: "YE",
	475: "YE", 477: "HK", 478: "BA",
	// Oceania
	501: "TF", 503: "AU", 506: "MM", 508: "BN", 510: "FM", 511: "PW", 512: "NZ", 514: "KH", 515: "KH", 516: "CX",
	518: "CK", 520: "FJ", 523: "CC", 525: "ID", 529: "KI", 531: "LA", 533: "MY", 536: "MP", 538: "MH", 540: "NC",
	542: "NU", 544: "NR", 546: "PF", 548: "PH", 550: "TL", 553: "PG", 555: "PN", 557: "SB", 559: "AS", 561: "WS",
	563: "SG", 564: "SG", 565: "SG", 566: "SG", 567: "TH", 570: "TO", 572: "TV", 574: "VN", 576: "VU", 577: "VU",
	578: "WF",
	// Africa
	601: "ZA", 603: "AO", 605: "DZ", 607: "TF", 608: "SH", 609: "BI", 610: "BJ", 611: "BW", 612: "CF", 613: "CM",
	615: "CG", 616: "KM", 617: "CV", 618: "TF", 619: "CI", 620: "KM", 621: "DJ", 622: "EG", 624: "ET", 625: "ER",
	626: "GA", 627: "GH", 629: "GM", 630: "GW", 631: "GQ", 632: "GN", 633: "BF", 634: "KE", 635: "TF", 636: "LR",
	637: "LR", 638: "SS", 642: "LY", 644: "LS", 645: "MU", 647: "MG", 649: "ML", 650: "MZ", 654: "MR", 655: "MW",
	656: "NE", 657: "NG", 659: "NA", 660: "RE", 661: "RW", 662: "SD", 663: "SN", 664: "SC", 665: "SH", 666: "SO",
	667: "SL", 668: "ST", 669: "SZ", 670: "TD", 671: "TG", 672: "TN", 674: "TZ", 675: "UG", 676: "CD", 677: "TZ",
	678: "ZM", 679: "ZW",
	// South America
	701: "AR", 710: "BR", 720: "BO", 725: "CL", 730: "CO", 735: "EC", 740: "FK", 745: "GF", 750: "GY", 755: "PY",
	760: "PE", 765: "SR", 770: "UY", 775: "VE",
}
