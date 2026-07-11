---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Vision
spec_version: 1.26.0622.1
---

# Vision

_6 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [POST `/vision/video/bulk-upload`](#post-vision-video-bulk-upload) — Bulk Upload Videos
- [POST `/vision/video/upload`](#post-vision-video-upload) — Upload Video
- [GET `/vision/videos/requests`](#get-vision-videos-requests) — Get Video Requests
- [POST `/vision/videos/requests`](#post-vision-videos-requests) — Create Video Requests
- [GET `/vision/videos/status`](#get-vision-videos-status) — Get Video Requests Status
- [POST `/vision/livestream/{registration}`](#post-vision-livestream-registration) — Video Livestream Requests

## POST `/vision/video/bulk-upload`

**Bulk Upload Videos**

This endpoint allows you to upload multiple videos from custom third party camera device(s). Note: This service is available exclusively for Cartrack Vision API enabled accounts. If you do not have access (HTTP 403), contact our sales representative.

### Request Body

The json data that needs to be processed

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object | **required** | Collection of vision video records to be created. May not have more than 100 items. |  |
| `webhook_url` | string |  | The webhook URL to receive the video bulk upload status.       A x-webhook-signature header will be sent with the webhook for you to verify the request. Please refer to webhook documentation in the introduction section for more details.       Sample webhook payload:  ```  [ { "registration": "ABC1234X", "videos": [ { "camera": "3", "start_timestamp": "2025-01-01 00:00:00", "end_timestamp": "2025-01-01 00:05:00", "errors": "The file is not of MP4 video format." }, { "camera": 2, "start_timestamp": "2025-01-01 00:00:00", "end_timestamp": "2025-01-01 00:05:00", "request_id": 15139 } ] }, { "registration": "DEF4321X", "videos": [ { "camera": "1", "start_timestamp": "2025-01-01 01:35:00", "end_timestamp": "2025-01-01 01:36:00", "request_id": 15140 } ] } ] ``` | "https://your-webhook-url.com/delivery-jobs-bulk-upload" |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |

**Content-Type:** `application/xml`




#### `401` — Unauthorized access. Authentication is required.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `403` — Access is forbidden. The user does not have permission to access this resource.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `404` — The requested resource was not found.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `422` — Validation failed for the input parameters.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `500` — Internal server error.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |


---

## POST `/vision/video/upload`

**Upload Video**

This endpoint allows you to upload video from custom third party camera device(s). Note: This service is available exclusively for Cartrack Vision API enabled accounts. If you do not have access (HTTP 403), contact our sales representative.

### Request Body

The json data that needs to be processed

**Content-Type:** `application/x-www-form-urlencoded`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `video_file` | string | **required** | Video file to be uploaded.    Only MP4 format with H.246 audio codec is accepted.     Note: Maximum accepted file size: 50 MB. |  |
| `registration` | `registration` | **required** |  |  |
| `start_timestamp` | `date` | **required** | The start timestamp of the video to be uploaded. | "2024-12-31 00:00:00" |
| `end_timestamp` | `date` | **required** | The end timestamp of the video to be uploaded.   Total duration limit to be within 5 minutes from start\_timestamp. | "2024-12-31 00:30:00" |
| `camera` | integer | null | **required** | The camera property specifies the channel from which the video should be tagged to. | 1 |
| `comments` | string |  | The comment to be included with the uploaded video.   Previously uploaded video with the same registration, start\_timestamp and end\_timestamp will be updated. | "Footage captured from XXX camera." |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |

#### `401` — Unauthorized access. Authentication is required.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `403` — Access is forbidden. The user does not have permission to access this resource.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `404` — The requested resource was not found.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `422` — Validation failed for the input parameters.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `500` — Internal server error.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |


---

## GET `/vision/videos/requests`

**Get Video Requests**

This endpoint retrieves the video clips downloaded from your vehicles. Note: This service is available exclusively for Cartrack Vision API enabled accounts. If you do not have access (HTTP 403), contact our sales representative.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[request_id][]` | optional | integer | null | The video request ID | 12345 |
| `filter[chassis_number]` | optional | `chassis-number` | Filter by chassis number, case insensitive, can be partial match |  |
| `filter[request_comment]` | optional | string | null | The video request comment | Video of an incident |
| `filter[camera]` | optional | integer | null | The number indicator of the camera responsible for capturing the video on a certain angle or position | 3 |
| `filter[status_id]` | optional | integer | The video request status.    | status\_id | description | | --- | --- | | 1 | Pending | | 2 | In progress | | 3 | Complete | | 4 | Cancelled | | 5 | Does not exist | | 6 | Timeout | | 3 |
| `filter[event_type_id]` | optional | integer | The event type.     | event\_type\_id | event\_description | | --- | --- | | 289 | ABSENCE\_LOITERING | | 282 | ACTION\_DETECTION | | 275 | CAMERA\_BUTTON | | 271 | CAMERA\_COVERED | | 286 | CAMERA\_STATUS | | 287 | CUSTOM | | 291 | CUSTOM\_OBJECT\_DETECTION | | 263 | DISTRACTION\_DETECTED | | 269 | EYE\_CLOSED\_DETECTED | | 280 | FACEMASK | | 283 | FACE\_RECOGNITION | | 266 | FATIGUE\_DETECTED | | 264 | FORWARD\_COLLISION\_WARNING | | 272 | HEADWAY\_MONITORING\_WARNING | | 288 | HIGH\_PRIORITY\_LINE\_CROSS | | 273 | LANE\_DEPARTURE\_WARNING | | 284 | LICENSE\_PLATE\_RECOGNITION | | 279 | LINE\_CROSS | | 212 | MOBILE\_EYE\_DELTA | | 278 | MULTI\_OBJECT | | 277 | NO\_SEATBELT\_DETECTED | | 276 | PASSENGER\_DETECTED | | 265 | PEDESTRIAN\_COLLISION\_WARNING | | 267 | PHONE\_DETECTED | | 285 | SAFETY\_HELMET\_DETECTION | | 268 | SMOKING\_DETECTED | | 281 | SOCIAL\_DISTANCING | | 290 | SUSPICIOUS\_BEHAVIOUR | | 274 | URBAN\_FORWARD\_COLLISION\_WARNING | | 270 | YAWNING\_DETECTED | | 263 |
| `filter[video_start_ts_from]` | optional | `date` | The earliest date and time to retrieve the videos based on video start timestamp | 2024-09-27 00:00:00 |
| `filter[video_start_ts_to]` | optional | `date` | The latest date and time to retrieve the videos based on video start timestamp | 2024-09-27 10:15:00 |
| `filter[video_end_ts_from]` | optional | `date` | The earliest date and time to retrieve the videos based on video end timestamp | 2024-09-27 00:00:00 |
| `filter[video_end_ts_to]` | optional | `date` | The latest date and time to retrieve the videos based on video end timestamp | 2024-09-27 10:15:00 |
| `filter[start_request_ts]` | optional | `date` | The earliest date and time to retrieve the videos based on video request timestamp | 2024-09-27 00:00:00 |
| `filter[end_request_ts]` | optional | `date` | The latest date and time to retrieve the videos based on video request timestamp | 2024-09-27 10:15:00 |
| `filter[start_updated_ts]` | optional | `date` | The earliest date and time to retrieve the videos based on video updated timestamp | 2024-09-27 00:00:00 |
| `filter[end_updated_ts]` | optional | `date` | The latest date and time to retrieve the videos based on video updated timestamp | 2024-09-27 10:15:00 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |
| `meta` | `pagination` |  |  |  |

#### `401` — Unauthorized access. Authentication is required.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `403` — Access is forbidden. The user does not have permission to access this resource.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `404` — The requested resource was not found.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `422` — Validation failed for the input parameters.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `500` — Internal server error.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |


---

## POST `/vision/videos/requests`

**Create Video Requests**

This endpoint creates a request of vehicle video clips. Note: This service is available exclusively for Cartrack Vision API enabled accounts. If you do not have access (HTTP 403), contact our sales representative.

### Request Body

JSON payload containing the data required for driver creation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `registration` | `registration` | **required** |  |  |
| `start_timestamp` | `date` | **required** | The requested video start timestamp |  |
| `duration` | integer | null | **required** | The video duration starting from the start timestamp, in seconds. | 10 |
| `camera` | array of integer | null | **required** | The camera property specifies the channel from which the video should be retrieved. You must indicate the desired camera channel, and the API will fetch the video from the DVR storage if it is still available. |  |
| `comments` | string | null |  | The request video comment. | "Requesting for camera 1 video." |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |

#### `401` — Unauthorized access. Authentication is required.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `403` — Access is forbidden. The user does not have permission to access this resource.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `404` — The requested resource was not found.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `422` — Validation failed for the input parameters.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `500` — Internal server error.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |


---

## GET `/vision/videos/status`

**Get Video Requests Status**

This endpoint retrieves the list of video request status. Note: This service is available exclusively for Cartrack Vision API enabled accounts. If you do not have access (HTTP 403), contact our sales representative.

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |

#### `401` — Unauthorized access. Authentication is required.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `403` — Access is forbidden. The user does not have permission to access this resource.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `404` — The requested resource was not found.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `422` — Validation failed for the input parameters.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `500` — Internal server error.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |


---

## POST `/vision/livestream/{registration}`

**Video Livestream Requests**

This endpoint returns the vehicle's livestream links. Note: This service is available exclusively for Cartrack Vision API enabled accounts. If you do not have access (HTTP 403), contact our sales representative.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The vehicle registration. | ABC1234X |

### Request Body

JSON payload containing the data required for livestream request.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `camera` | array of integer | **required** | The camera streaming the live video on a certain angle or position. |  |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |

#### `401` — Unauthorized access. Authentication is required.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `403` — Access is forbidden. The user does not have permission to access this resource.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `404` — The requested resource was not found.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `422` — Validation failed for the input parameters.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `500` — Internal server error.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |


---

