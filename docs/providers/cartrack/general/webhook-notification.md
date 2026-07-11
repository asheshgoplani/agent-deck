---
source: https://developer.cartrack.com/docs/fleet-api-general/webhook-notification
title: Webhooks
---

# Webhooks

# Webhooks

The Cartrack Fleet API provides webhook notifications to allow your system to receive real-time updates for various events. Webhooks enable seamless integration by automatically pushing event data to your specified endpoint whenever an event occurs.

### Supported Webhook Flows​

As of today, the API supports a limited set of webhook events. Webhooks are not available for all system events.

Currently supported webhook flow:

  * **Bulk Upload Delivery Jobs** : Triggered when the jobs creation process is completed

No other webhook events or data schemas are supported at this time. Additional webhook flows may be introduced in future releases.

### Security and Verification​

All webhook requests sent from Cartrack will include the X-Webhook-Signature HTTP header. This signature allows you to verify that the request is genuinely from Cartrack and has not been tampered with.

The signature is generated using the HMAC-SHA256 algorithm with the following components:

  * Secret Key: Your API key.
  * Payload: The raw JSON request body (as a string before any decoding or parsing).

#### Signature Generation Process​

  1. Encode the JSON payload (raw content) as a UTF-8 string.
  2. Hash the payload using HMAC-SHA256 with your API key as the secret.

Example (PHP):


    $secret = 'your_api_key';


    $payload = file_get_contents('php://input');


    $signature = hash_hmac('sha256', json_encode($payload), $secret);





    if (hash_equals($signature, $_SERVER['X-Webhook-Signature'])) {


        // Verified request


    } else {


        // Invalid signature


    }


It is recommended to always verify the webhook signature before processing any webhook request to ensure data integrity and security.
