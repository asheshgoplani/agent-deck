---
source: https://developer.cartrack.com/docs/fleet-api-general/services/delivery-job-services
title: Delivery Job Services
---

# Delivery Job Services

# Delivery Job Services

This page explains how to create delivery jobs with the Fleet API and how to confirm whether submitted jobs were created successfully.

## Acronyms and Terms​

  * **API** : Application Programming Interface.
  * **ERP** : Enterprise Resource Planning.
  * **HTTP** : Hypertext Transfer Protocol.

## Which Endpoint Should I Use?​

Use this mapping based on your job creation volume and feedback needs:

  * **High-volume job creation (batch uploads)** :
    * [Bulk Upload Delivery Jobs](<https://developer.cartrack.com/docs/fleet-api/bulk-upload-delivery-jobs>)
  * **Single or low-volume job creation (immediate response)** :
    * [Create a Delivery Job](<https://developer.cartrack.com/docs/fleet-api/create-a-delivery-job>)

## 1) Bulk Upload Delivery Jobs​

### What It Means​

Submit multiple jobs in a single request when you need to push many deliveries from ERP or dispatch systems.

### Confirmation and Notifications​

The bulk upload service supports a `webhooks_url` field. After processing is completed, Cartrack sends a webhook callback so your system can confirm the upload outcome and reconcile what was created. For webhook setup, signature verification, and security guidance, see [Webhooks](</docs/fleet-api-general/webhook-notification>).

### Purpose​

  * Best option for scheduled or large delivery imports.
  * Gives operations teams a reliable confirmation flow instead of assuming all jobs were created.
  * Helps identify partial-creation scenarios quickly.

### Developer Considerations​

  * Treat the request as an asynchronous flow.
  * Use webhook callbacks as the source of truth for completion status.
  * Compare your submitted jobs with callback results and trigger retry/escalation for jobs that were not created.

## 2) Create a Delivery Job​

### What It Means​

Create one delivery job per request with immediate API feedback.

### Confirmation and Errors​

If the job cannot be created, the API returns a non-`200` HTTP status code together with an error message that explains the failure.

### Purpose​

  * Best for interactive flows where users create one job at a time.
  * Failures are visible immediately and can be surfaced directly to support or dispatch users.

### Developer Considerations​

  * Validate the HTTP response status for every request.
  * Handle non-`200` responses as explicit creation failures.
  * Log and return the API error message so operations teams can resolve data issues quickly.

## Recommended Integration Pattern​

  1. Keep a client-side reference for each submitted job (from ERP/dispatch).
  2. Use bulk upload with `webhooks_url` for high-volume imports.
  3. Track callback outcomes and reconcile expected vs created jobs.
  4. Handle all non-`200` responses from single-job creation as failures.
  5. Retry or escalate failed jobs through your internal support workflow.
