---
source: https://developer.cartrack.com/docs/fleet-api-general/services/vision-services
title: Vision Services
---

# Vision Services

# Vision Services

This page explains how to interact with Cartrack Vision cameras through the Fleet API, covering video clip requests, status tracking, livestreaming, and custom camera uploads.

Vision API subscription required

This service is available exclusively for accounts with the **Cartrack Vision API** option enabled. If you receive HTTP 403, contact your Cartrack customer success manager to enable access. After activation, allow 5 to 10 minutes for the configuration to propagate before making API calls.

## Which Endpoint Should I Use?​

Use this mapping based on what you want to accomplish:

Goal| Endpoint
---|---
Request a video clip from a vehicle camera| [Create Video Requests](<https://developer.cartrack.com/docs/fleet-api/create-video-requests>)
Retrieve downloaded video clips| [Get Video Requests](<https://developer.cartrack.com/docs/fleet-api/get-video-requests>)
Check the processing status of video requests| [Get Video Requests Status](<https://developer.cartrack.com/docs/fleet-api/get-video-requests-status>)
Get a real-time livestream link for a vehicle| [Video Livestream Requests](<https://developer.cartrack.com/docs/fleet-api/video-livestream-requests>)
Upload a single video from a custom camera device| [Upload Video](<https://developer.cartrack.com/docs/fleet-api/upload-video>)
Upload multiple videos from custom camera device(s)| [Bulk Upload Videos](<https://developer.cartrack.com/docs/fleet-api/bulk-upload-videos>)

## 1) Requesting and Retrieving Video Clips​

### How It Works​

A video request is not an immediate download. When you submit a request, you are instructing the camera device to locate the requested footage on its local storage and upload it to the Cartrack server. The clip only becomes available to download once the camera has completed that upload.

This means the full flow depends on two things outside the API's control: the camera must be online with sufficient network coverage, and the requested footage must still exist on the device's local storage. If the camera has poor or no connectivity at the time of the request, it will retry automatically and the clip will remain in Pending until it can be fully uploaded.

### How Video Requests Are Created​

Video requests can be created from three different sources. `GET /vision/videos/requests` returns all requests regardless of origin. You will see requests you did not create via the API.

Source| How
---|---
Fleet API| `POST /vision/videos/requests` (programmatic, from your integration)
Camera AI| Automatic. The DVR triggers a request when it detects a configured event (fatigue, distraction, cell phone usage, etc.)
Fleetweb| Manual. A fleet operator requests a clip through the web portal

### Clip Lifecycle​

After a request is created, its `status_id` progresses through several states. The `url` field is `null` until the clip reaches status `3` (Complete).

Key status codes to handle in your integration:

status_id| Label| Meaning
---|---|---
1| Pending| Request queued, waiting for camera to upload
2| In Progress| Camera is uploading the footage
3| Complete| Footage is ready. `url` is populated
5| Does Not Exist| Requested time range not found on camera storage
6| Timeout| Camera did not respond within the allowed window

### Limitations​

**Network coverage.** The camera must be online and have sufficient network connectivity to upload footage. If the vehicle is in a low-coverage area when the request is placed, it will remain in Pending until coverage is restored. There is no guarantee of when, or whether, that happens.

**FIFO local storage.** DVR cameras store footage locally with a fixed capacity. When storage is full, the oldest footage is overwritten. If you place a request for footage from several days ago and the camera has been recording continuously since, that segment may no longer exist on the device. This results in a `Does Not Exist` (status 5) or `Timeout` (status 6) outcome. Request footage as soon as possible after the event of interest.

### Polling Pattern​

To track a clip from creation to completion, poll `GET /vision/videos/requests` and inspect the `status_id` field on each request. Poll at a reasonable interval; every 30 to 60 seconds is sufficient. Stop polling when `status_id` reaches a terminal state (`3`, `5`, or `6`).

`GET /vision/videos/status` is a static reference table that returns the full list of status codes and their descriptions. It is not a per-request status endpoint and does not need to be polled.

## 2) Video Livestream​

### What It Means​

Request a real-time livestream link for a specific vehicle. The API returns a URL your application can use to display a live camera feed.

### Error: Camera Not Online​

If the vehicle's DVR camera is currently offline, the API returns:


    HTTP 503


    {"error":{"code":503,"message":"Digital Video Recorder is not online"}}


This indicates the camera device itself is unreachable — the Fleet API is functioning normally. **Do not treat this as a transient server error and retry in a loop.** Instead, surface it to the user as a camera availability issue and let the user decide when to retry.

### Developer Considerations​

  * Livestream links are typically short-lived. Do not cache them for reuse across sessions.
  * The link is tied to the vehicle registration passed in the path (`/vision/livestream/{registration}`).
  * If a vehicle has multiple cameras, the response may include multiple stream links.
  * Handle 503 from this endpoint as a camera state issue, not a server failure. Implement a backoff or let the user trigger the retry manually.

## 3) Uploading Video from Custom Camera Devices​

### What It Means​

If your fleet uses third-party camera hardware that is not a native Cartrack Vision device, you can push recorded video into Cartrack Vision using the upload endpoints. This allows non-Cartrack cameras to be integrated into the same video management workflow.

### Single vs. Bulk Upload​

  * **Single upload** (`POST /vision/video/upload`): Upload one video file per request. Best for real-time or low-volume ingest.
  * **Bulk upload** (`POST /vision/video/bulk-upload`): Upload multiple video files in a single request. Best for batch ingest from a device that stores footage locally and syncs periodically.

### Developer Considerations​

  * Ensure the vehicle registration in the upload payload matches an active contract with the Vision API VAS enabled, or the upload will be rejected.
  * For bulk uploads, treat each file's result individually — a partial failure does not necessarily reject the entire batch.
  * Validate file format and size requirements against the API schema before uploading to avoid unnecessary rejections.

## Recommended Integration Pattern​

  1. Confirm the Vision API VAS is active on the target account before integrating (test with any Vision endpoint; a 403 means the VAS is not yet configured).
  2. For clip retrieval: submit the request, poll for status, then fetch the clip once it reaches a ready state.
  3. For livestream: request a fresh link per session and do not reuse expired links.
  4. For custom camera uploads: match video metadata (vehicle registration, timestamp) precisely to ensure footage is correctly associated in the Cartrack platform.
