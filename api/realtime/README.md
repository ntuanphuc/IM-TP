# ChatTP WebSocket pilot

Connect to `GET /v1/ws` over WSS. Browser sends `{"type":"auth","data":{"token":"<access JWT>"}}` as the first client frame. Initial `auth.ok` includes `data.cursor`, the global event watermark. To resume after reconnecting, send the latest cursor previously received with `{"type":"resume","data":{"cursor":123}}`. Send a fresh token in another auth frame when requested, using the same session. `ping` is answered with `pong`.

An event has `type`, numeric `id` (the cursor), `channel_id` and `data.id`. Events invalidate REST state. Types include message.new, message.updated, message.deleted, reaction.changed, read.updated, file.updated and broadcast.new. Clients deduplicate by event ID. Only members of the affected channel receive an event. The initial cursor is a watermark. Use the stored cursor for replay and refresh the view after the handshake.
