---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Alerts
spec_version: 1.26.0622.1
---

# Alerts

_7 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/alerts`](#get-alerts) — Get Alerts
- [DELETE `/alerts/{id}`](#delete-alerts-id) — Delete an Alert
- [POST `/alerts/geofences`](#post-alerts-geofences) — Create Geofence Alert
- [POST `/alerts/ignition`](#post-alerts-ignition) — Create Ignition Alert
- [GET `/alerts/notifications`](#get-alerts-notifications) — Get Alerts Notifications
- [GET `/alerts/notifications/types`](#get-alerts-notifications-types) — Get Alerts Notification Types
- [POST `/alerts/sensors`](#post-alerts-sensors) — Create Sensor Alert

## GET `/alerts`

**Get Alerts**

This endpoint fetches all alerts associated with the account.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[id]` | optional | string | Optional filter to retrieve by identification number. | 62462fcf-0938-11ec-8c4d-a4bf016cd6b2 |
| `filter[name]` | optional | string | Optional filter to retrieve by name. | This is an API alert for Gofence 0724 |
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

## DELETE `/alerts/{id}`

**Delete an Alert**

This endpoint deletes an existing alert identified by its unique UUID.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | string | The ID of the alert to be deleted. | 123e4567-e89b-12d3-a456-426614174000 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  | An empty object | {} |
| `meta` | object |  | This metadata will contain a message |  |

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

## POST `/alerts/geofences`

**Create Geofence Alert**

This endpoint creates an alert for vehicles entry or exit or both into a geofence or a group of geofences.  
 Please note that you can create up to 50 alerts. If you need to create more, you must first remove some of the existing alerts.

### Request Body

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `name` | string | **required** | The name of the geofence alert. | "Geofence alert for vehicle ABC-1234 and ABC-5678" |
| `registrations` | array of string |  | An array of vehicle registration numbers. This field is mutually exclusive with the vehicles field. | ["ABCE8901", "XYZ1234"] |
| `geofence_trigger_id` | integer |  | The trigger is currently limited to ids 1, 2 and 3. - 1 to trigger on geofence entries - 2 to trigger on geofence exits - 3 to trigger on both geofence entries and exists | 1 |
| `geofence_ids` | array of string |  | The array list of unique identifier (UUID) of the geofences. **Required only if geofence\_group\_ids is not given.** It can be combined with geofence\_ids and geofence\_group\_ids, meaning you can create an alert for multiple geofence\_ids and multiple geofence\_group\_ids. | ["123e4567-e89b-12d3-a456-426614174000", "234e4567-e89b-12d3-a456-426614174000"] |
| `geofence_group_ids` | array of integer |  | The array list of unique identifier of the geofence group. **Required if geofence\_ids is not given**. It can be combined with geofence\_ids and geofence\_group\_ids, meaning you can create an alert for multiple geofence\_ids and multiple geofence\_group\_ids. | [12345, 67891] |
| `contact_type` | object | **required** | This object must contain the contact type information such as, contact\_type\_id, values, and priority\_id. |  |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  | An empty object |  |
| `meta` | object |  | This metadata will contain a message |  |

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

## POST `/alerts/ignition`

**Create Ignition Alert**

This endpoint creates an alert for vehicles ignition ON or OFF events.  
Please note that you can create up to 50 alerts. If you need to create more, you must first remove some of the existing alerts.

### Request Body

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `name` |  | **required** | The name of the ignition alert. | "Ignition alert for vehicle ABC1234 and ABC5678" |
| `registrations` | array | null |  |  | ["ABC1234", "ABC5678"] |
| `ignition_trigger_id` | `ignition-trigger-id` |  |  |  |
| `geofence_id` | `geofence-id` |  |  |  |
| `geofence_group_id` | integer |  | The unique identifier of the geofence group. | 12345 |
| `inside_geofence` | boolean |  | This parameter determines the location condition for reporting the ignition event alert. If set to true, the alert will be triggered only when the ignition event occurs inside the specified geofence(s). If set to false, the alert will be triggered only when the ignition event occurs outside the specified geofence(s). **Required only if geofence\_id or geofence\_group\_id is given.** | true |
| `contact_type` | object | **required** | This object must contain the contact type information such as, contact\_type\_id, values, and priority\_id. |  |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  | An empty object |  |
| `meta` | object |  | This metadata will contain a message |  |

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

## GET `/alerts/notifications`

**Get Alerts Notifications**

Fetch notifications of your account between provided date range.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[contact_type]` | optional | string | Filter by contact type | E-Mail Message |
| `filter[alert_type]` | optional | string | Filter alert trigger description | IGNITION_ON_OFF |
| `filter[status]` | optional | string | Filter by alert status | Email sent |
| `filter[notification_contact]` | optional | string | Filter alert by notification contact | mark.steven@example.com |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | This array returns the list of notifications |  |
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

## GET `/alerts/notifications/types`

**Get Alerts Notification Types**

Fetch notification alert types.

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of string |  | This array returns the list of notifications trigger types | ["AFTER_HOURS_MOVEMENT", "ARM_ALERT", "COOLANT_TEMPERATURE", "DRIVER_ID", "DRIVER_LICENSE_EXPIRED", "DYNAMIC_BIT_EVENT", "ENGINE_DIAGNOSTIC", "ENGINE_TEMPERATURE", "ETOLL_GANTRY", "EXCESSIVE_DRIVING", "FUEL_GAIN", "FUEL_GAIN_LOSS", "GEOFENCE_ALERTS_ASSET_TRACKER", "GEOFENCE_ALERTS_GEOFENCE_DURATION", "GEOFENCE_ALERTS_TEMP1_HIGH_WITH_IGNITION_ON", "GEOFENCE_ALERTS_TEMP1_LOW_WITH_IGNITION_ON", "GEOFENCE_ALERTS_TEMP2_HIGH_WITH_IGNITION_ON", "GEOFENCE_ALERTS_TEMP2_LOW_WITH_IGNITION_ON", "GEOFENCE_ALERTS_TEMP3_HIGH_WITH_IGNITION_ON", "GEOFENCE_ALERTS_TEMP3_LOW_WITH_IGNITION_ON", "GEOFENCE_ALERTS_TEMP4_HIGH_WITH_IGNITION_ON", "GEOFENCE_ALERTS_TEMP4_LOW_WITH_IGNITION_ON", "GEOFENCE_ENTERING_LEAVING", "HARSH_EVENTS", "HIGH_RPM", "IDLE", "IGNITION_ON_OFF", "MAX_SPEED_EXCEEDED", "MOTION", "OVERREV", "POWER_ON_OFF", "RECOVERY", "REMINDERS", "ROAD_SPEED", "ROUTE_DEVIATION", "ROUTE_DEVIATION_CANCELLED", "ROUTE_DEVIATION_RETURNED", "ROUTE_NOT_ENDED_ON_TIME", "ROUTE_NOT_STARED_WITHIN_TIME", "ROUTE_SLOW_PROGRESS", "ROUTE_START_END", "SENSORS", "SPEEDING", "STATIC_BIT_EVENT", "STATIONARY", "TEMPERATURE_DIAGNOSTIC", "TRIP_SUMMARY", "UNKOWN_TRIGGER", "VEHICLE_LICENSE_EXPIRED", "VISION_FACILITIES_ABSENCE_LOITERING", "VISION_FACILITIES_ACTION_DETECTION", "VISION_FACILITIES_CAMERA_STATUS", "VISION_FACILITIES_CUSTOM", "VISION_FACILITIES_CUSTOM_OBJECT_DETECTION", "VISION_FACILITIES_FACEMASK", "VISION_FACILITIES_FACE_RECOGNITON", "VISION_FACILITIES_HIGH_PRIORITY_LINE_CROSS", "VISION_FACILITIES_LICENSE_PLATE_RECOGNITION", "VISION_FACILITIES_LINE_CROSS_OBJECT", "VISION_FACILITIES_MULTI_OBJECT", "VISION_FACILITIES_SAFETY_HELMET_DETECTION", "VISION_FACILITIES_SOCIAL_DISTANCING", "VISION_FACILITIES_SUSPICIOUS_BEHAVIOUR", "VISION_VEHICLES_BUTTON", "VISION_VEHICLES_CAMERA_COVERED", "VISION_VEHICLES_DISTRACTION", "VISION_VEHICLES_EYES_CLOSED", "VISION_VEHICLES_EYE_DELTA", "VISION_VEHICLES_FATIGUE", "VISION_VEHICLES_FORWARD_COLLISION", "VISION_VEHICLES_HEADWAY_MONITORING", "VISION_VEHICLES_LANE_DEPARTURE", "VISION_VEHICLES_NO_SEATBEALT", "VISION_VEHICLES_PASSENGER", "VISION_VEHICLES_PEDESTRIAN_COLLISION", "VISION_VEHICLES_PHONE", "VISION_VEHICLES_SMOKE", "VISION_VEHICLES_URB_FORWARD_COLLISION", "VISION_VEHICLES_YAWN", "ZONE_ARRIVE_DEPART"] |

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

## POST `/alerts/sensors`

**Create Sensor Alert**

This endpoint creates an alert for vehicle sensor events.  
Please note that you can create up to 50 alerts. If you need to create more, you must first remove some of the existing alerts.

### Request Body

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `name` |  | **required** | The name of the sensor alert. | "Sensor alert for vehicle ABC1234 and ABC5678" |
| `registrations` | array of string | **required** |  | ["ABC1234", "ABC5678"] |
| `sensor_type` | string | **required** | The type of sensor for the alert. - `PTO` (Power Take-Off) triggers on auxiliary equipment engagement. - `PANIC` triggers on a panic button press.  <table>   <tr>     <th>Sensor Type</th>     <th>Support Trigger Types</th>   </tr>   <tr>     <td>PTO</td>     <td>ON, OFF</td>   </tr>   <tr>     <td>PANIC</td>     <td>ON, OFF</td>   </tr> </table> | "PTO" |
| `trigger_types` | array of string | **required** |  |  |
| `cooldown_period_seconds` | integer |  | The cooldown period in seconds for the alert. This parameter defines the minimum time interval between two consecutive alerts for the same sensor event. | 3600 |
| `geofence_id` | `geofence-id` |  | The unique identifier of the geofence. Can be provided together with `geofence_group_id`, both are stored independently and the alert applies to both. |  |
| `geofence_group_id` | integer |  | The unique identifier of the geofence group. Can be provided together with `geofence_id`, both are stored independently and the alert applies to both | 12345 |
| `inside_geofence` | boolean |  | This parameter determines the location condition for  reporting the sensor event alert. If set to true, the alert  will be triggered only when the sensor event occurs inside the specified geofence(s).  If set to false, the alert will be  triggered only when the sensor event occurs outside the  specified geofence(s).  **Required only if geofence\_id or geofence\_group\_id is given.** | true |
| `contact_type` | object | **required** | This object must contain the contact type information such as, contact\_type\_id, values, and priority\_id. |  |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  | The created alert details |  |
| `meta` | object |  | This metadata will contain a message |  |

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

