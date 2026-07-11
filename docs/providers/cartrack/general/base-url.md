---
source: https://developer.cartrack.com/docs/fleet-api-general/base-url
title: Base URLs
---

# Base URLs

# Base URLs

Correct base URLs ensure optimal performance, correct data routing, and compliance with regional requirements. Cartrack accounts are hosted by region — using the wrong regional endpoint for an account can prevent authentication and will typically return HTTP 401 Unauthorized. Use the region-specific endpoint for the account you are working with.

## Base URL template​


    https://fleetapi-<region>.cartrack.com/rest/vehicles

  * Replace `<region>` with the two-letter country code (ISO alpha-2).
  * Example: `https://fleetapi-za.cartrack.com/rest/vehicles`.

## Supported countries (code — name)​

Domain note:

  * `ke` (Kenya) and `sa` (Kingdom of Saudi Arabia) use the `karooooo.com` domain.

  * All other supported countries use the `cartrack.com` domain.

  * au — Australia

  * bw — Botswana

  * es — Spain

  * hk — Hong Kong

  * id — Indonesia

  * ke — Kenya

  * kh — Cambodia

  * me — Middle East

  * mw — Malawi

  * my — Malaysia

  * mz — Mozambique

  * na — Namibia

  * ng — Nigeria

  * nz — New Zealand

  * ph — Philippines

  * pl — Poland

  * pt — Portugal

  * rw — Rwanda

  * sa — Kingdom of Saudi Arabia

  * sg — Singapore

  * sw — Swaziland

  * th — Thailand

  * tz — Tanzania

  * vn — Vietnam

  * za — South Africa

  * zm — Zambia

  * znz — Zanzibar

  * zw — Zimbabwe

## Quick example (curl)​


    curl -H "Authorization: Bearer <TOKEN>" "https://fleetapi-za.cartrack.com/rest/vehicles"

Notes:

  * Verify which country code is associated with your account (ask your Fleetweb Administrator).
  * For the canonical API reference see the [API References](</docs/applications/fleet-api>) page.

![Example of base URL](/assets/images/base_urls-ce7d8fc5ead30936c966c2e742ac0a15.png)
