---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Delivery Jobs
spec_version: 1.26.0622.1
---

# Delivery Jobs

_9 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [POST `/delivery/jobs/bulk-upload`](#post-delivery-jobs-bulk-upload) — Bulk Upload Delivery Jobs
- [PUT `/delivery/jobs/{job_id}/complete`](#put-delivery-jobs-job-id-complete) — Complete a Delivery Job
- [GET `/delivery/jobs`](#get-delivery-jobs) — Retrieve Delivery Jobs
- [POST `/delivery/jobs`](#post-delivery-jobs) — Create a Delivery Job
- [GET `/delivery/jobs/{job_id}`](#get-delivery-jobs-job-id) — Retrieve Delivery Job Details
- [PUT `/delivery/jobs/{job_id}`](#put-delivery-jobs-job-id) — Update a Delivery Job
- [DELETE `/delivery/jobs/{job_id}`](#delete-delivery-jobs-job-id) — Delete a Delivery Job
- [POST `/delivery/optimize/routes`](#post-delivery-optimize-routes) — Delivery Jobs Optimization
- [PUT `/delivery/jobs/assign/{driver_id}`](#put-delivery-jobs-assign-driver-id) — Reassign Jobs to a Delivery Driver

## POST `/delivery/jobs/bulk-upload`

**Bulk Upload Delivery Jobs**

This endpoint allows you to bulk create delivery jobs (up to 1000 jobs per request) from an Excel/CSV file. Parameters in the file are exactly the same as POST /delivery/jobs endpoint, you may refer to it for more details.

### Request Body

**Content-Type:** `multipart/form-data`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `file` |  | **required** | Excel/CSV file containing delivery jobs to be uploaded.    Only .xlsx or .csv format is accepted with a maximum accepted file size of 10 MB.   The file format will follow versioning, the current version is v1.        The following columns for version v1 are required:  * reference\_number * job\_type\_id * delivery\_driver\_id * delivery\_driver\_name * schedule\_type\_id * scheduled\_delivery\_ts * labels * subuser\_id * allowed\_to\_start\_at * send\_to\_driver\_at * stops * items      **reference\_number** is recommended for webhook integration. You'll receive notification with this field as a reference for jobs creation status.       **stops** and **items** accepts json. Please refer to the example file linked in the introduction section for more details.      Example file formats for version v1:  * CSV file: [Click to download](/files/delivery_api_import_file.csv) * JSON file: [Click to download](/files/delivery_api_import_file.json) * Excel file: [Click to download](/files/delivery_api_import_file.xlsx) |  |
| `webhook_url` | string |  | The webhook URL to receive the delivery jobs bulk upload status.       A x-webhook-signature header will be sent with the webhook for you to verify the request. Please refer to webhook documentation in the introduction section for more details.       Sample webhook payload:    ```  [ { 'reference_number': 'CART202502260001', 'job_id': 119582 }, { 'reference_number': 'CART202502260002', 'errors': { 'job_type_id': [ 'The selected job_type_id is invalid.' ] } } ] ``` | "https://your-webhook-url.com/delivery-jobs-bulk-upload" |
| `template_version` | string |  | The version of the template used for the job bulk upload. This is used to ensure compatibility with the expected file format.       If not provided, the default version will be used. | "v1" |

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

## PUT `/delivery/jobs/{job_id}/complete`

**Complete a Delivery Job**

Completes an assigned job. Remaining completion timestamps will be set to the time of the API call

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `job_id` | **required** | integer | Unique identifier of the delivery job to be completed | 123 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  | {} |
| `meta` | object |  |  |  |

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

## GET `/delivery/jobs`

**Retrieve Delivery Jobs**

Fetches a list of all delivery jobs, with optional filtering based on various criteria.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[job_id]` | optional | `id` | Optional filter to retrieve jobs by their unique ID. | 1 |
| `filter[order_id]` | optional | string | Optional filter to retrieve jobs by associated order ID. | 20210908000021 |
| `filter[schedule_type_id]` | optional | `schedule-type-id` | Optional filter to retrieve jobs based on schedule type (1 = ASAP, 2 = Scheduled, 3 = Unscheduled). | 1 |
| `filter[job_status_id]` | optional | `job-status-id` | Optional filter to retrieve jobs by their status (2 = Assign Later, 3 = Rejected/Failed, 4 = Assigned, 5 = Completed). | 2 |
| `filter[create_ts_from]` | optional | `date` | Optional filter to retrieve jobs created on or after a specific date and time. | "2021-10-01 00:00:00" |
| `filter[create_ts_to]` | optional | `date` | Optional filter to retrieve jobs created on or before a specific date and time. | 2021-10-31 or 2021-10-31 23:59:59 |
| `filter[scheduled_delivery_ts_from]` | optional | `date` | Optional filter to retrieve jobs with a scheduled delivery time starting from a specific date and time. | "2021-10-01 00:00:00" |
| `filter[scheduled_delivery_ts_to]` | optional | `date` | Optional filter to retrieve jobs with a scheduled delivery time up until a specific date and time. | "2021-10-31 23:59:59" |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |
| `filter[item_tracking_number]` | optional | `item-tracking-number` | Optional filter to retrieve jobs by their tracking number. | TRACK-123456-SIN2 |
| `filter[driver_id]` | optional | string | Optional filter to retrieve jobs assigned to a specific driver, identified by UUID. | 39d22470-1ad7-11ee-bfe8-f241fc6d518a |
| `filter[reference_number]` | optional | string | Optional filter to retrieve jobs by their reference number. | ABC123 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `delivery-job-response` |  |  |  |
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

## POST `/delivery/jobs`

**Create a Delivery Job**

This endpoint initiates a new delivery job and supports two job types: One-Stop job (job_type_id = 3) and Collection and Dropoff job (job_type_id = 1). For a One-Stop Delivery, it is required to specify only one destination. In contrast, the Collection and Dropoff job type necessitates exactly two designated points - one for collection and one for the final dropoff.

### Request Body

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `job_type_id` | `job-type-id` | **required** |  |  |
| `plan_id` | integer | null |  | Provide the plan\_id to assign the job to a plan.       When this is provided, the following fields must be omitted:   `delivery\_driver\_id` `delivery\_driver\_name` `schedule\_type\_id` `scheduled\_delivery\_ts`. | 12345 |
| `delivery_driver_id` | string | null |  | This field is only applicable to job creation without a plan\_id.       Provide this or delivery\_driver\_name, not both. If both are null, job status is set to 2 (Assign Later) or assigned to a subuser. | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `delivery_driver_name` | string | null |  | This field is only applicable to job creation without a plan\_id.       Provide this or delivery\_driver\_id, not both. If both are null, job status is set to 2 (Assign Later) or assigned to a subuser.       Case-insensitive exact match on "first\_name last\_name". Must be unique in your driver pool, assignment fails if duplicates exist. | "Steven Mark" |
| `schedule_type_id` | `schedule-type-id` |  |  |  |
| `scheduled_delivery_ts` | `date` |  | This field is only applicable to job creation without a plan\_id.       The delivery scheduled timestamp is required for `schedule\_type\_id` 2, but not applicable for 1 or 3. Type 1 automatically sets it to the next available schedule, while type 3 sets it to null. | "2021-09-11 11:40:36" |
| `reference_number` | string |  | Reference number | "ABC123" |
| `items` | array of object |  | Items of this job |  |
| `stops` | array of  | **required** | Add one stop or two stops to the delivery job. For job_type_id 3, this array must have exactly one stop. For job_type_id 1, it must have exactly two elements. |  |
| `special_equipment` | array of integer |  | Add equipment IDs to the delivery job. |  |
| `labels` | array of string |  | Add custom labels to your job |  |
| `subuser_id` | string | null |  | Indicate the subuser\_id to assign the job to a subuser.      This field may be overwritten if a plan containing subuser target driver is provided. | "ba1d07d2-31aa-11ee-9a58-506b8dbc8dfb" |
| `allowed_to_start_at` | string |  | Allowed to start timestamp | "2021-09-18 12:30:00" |
| `send_to_driver_at` | string |  | Send to driver timestamp | "2021-09-18 12:30:00" |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` |  |  |  |  |

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

## GET `/delivery/jobs/{job_id}`

**Retrieve Delivery Job Details**

Fetches the details of a specific delivery job.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `job_id` | **required** | integer | Unique identifier of the delivery job whose details are being retrieved | 123 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` |  |  |  |  |

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

## PUT `/delivery/jobs/{job_id}`

**Update a Delivery Job**

Updates the details of an existing delivery job.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `job_id` | **required** | integer | Unique identifier of the delivery job to be updated | 123 |

### Request Body

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `delivery_driver_id` | string | null |  | Provide this or delivery_driver_name, not both. If both are null, job status is set to 2 (Assign Later) or assigned to a subuser. | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `delivery_driver_name` | string | null |  | Provide this or delivery\_driver\_id, not both. If both are null, job status is set to 2 (Assign Later) or assigned to a subuser.       Case-insensitive exact match on "first\_name last\_name". Must be unique in your driver pool, assignment fails if duplicates exist. | "Steven Mark" |
| `schedule_type_id` | `schedule-type-id` |  |  |  |
| `scheduled_delivery_ts` | string |  | The delivery scheduled timestamp is required for schedule\_type\_id 2, but not applicable for 1 or 3.    Type 1 automatically sets it to the next available schedule, while type 3 sets it to null. | "2023-01-01 12:00:00" |
| `reference_number` | string | null |  | Your custom reference number | "ABC123" |
| `items` | array of object |  | IMPORTANT: When item object is removed, it'll be deleted in the database |  |
| `stops` | array of  |  | IMPORTANT: When stop object is removed, it'll be deleted in the database |  |
| `subuser_id` | string | null |  | Subuser ID | "72856440-1047-11ec-a6d3-a4bf016cd6b2" |
| `labels` | array of string |  | Labels for the job |  |
| `allowed_to_start_at` | string |  | Allowed to start timestamp | "2021-09-18 12:30:00" |
| `send_to_driver_at` | string |  | Send to driver timestamp | "2021-09-18 12:30:00" |
| `special_equipment` | array of integer |  | Add equipment IDs to the delivery job. |  |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` |  |  |  |  |

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

## DELETE `/delivery/jobs/{job_id}`

**Delete a Delivery Job**

Deletes a specified delivery job.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `job_id` | **required** | integer | Unique identifier of the job to be deleted | 123 |

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `force` | optional | boolean | The force flag enables the forced deletion of a job.     **Warning**- This action will delete the job and remove it from the driver's app, even if the job has already been assigned to a driver. | True |
| `title` | optional | string | The custom title for the job deletion. If not provided, a default title will be used. |  |
| `body` | optional | string | The custom body for the job deletion. If not provided, a default body will be used. |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  | {} |
| `meta` | object |  |  |  |

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

## POST `/delivery/optimize/routes`

**Delivery Jobs Optimization**

This endpoint suggests delivery job assignments to available drivers based on selected optimization constraints. The system intelligently matches jobs to drivers to improve delivery efficiency while considering rules such as time windows and equipment requirements. The suggested assignments are recommendations only and do not automatically assign the jobs to drivers.

### Request Body

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `job_ids` | array of integer | **required** | Array of job IDs to be optimized |  |
| `driver_ids` | array of string | **required** | Array of driver IDs available for assignment |  |
| `enable_capacity_constraints` | boolean | null |  | Whether to enable capacity constraints. |  |
| `enable_time_window_constraints` | boolean | null |  | Whether to enable time window constraints. |  |
| `enable_equipment_constraints` | boolean | null |  | Whether to enable equipment constraints. |  |
| `version` | string | null |  | The version of the optimization algorithm model to use. Default is v1 if not specified. | "v1" |

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

## PUT `/delivery/jobs/assign/{driver_id}`

**Reassign Jobs to a Delivery Driver**

Reassigns specified jobs to a different delivery driver. Reassigning jobs updates the job's `assigned_ts` to the current server timestamp.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `driver_id` | **required** | string | Provide either **delivery\_driver\_id** (a unique UUID) or **delivery\_driver\_name** (case-insensitive exact match on "first\_name last\_name").       driver\_name must be unique in your driver pool, assignment fails if duplicates exist. | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |

### Request Body

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `job_ids` | array of integer |  | Array of job IDs to be reassigned | [61, 22, 358] |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  | {} |
| `meta` | object |  |  |  |

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

