# Matrix Specification v1.19 --- HTTP endpoint index

Source: [Matrix Specification v1.19](https://spec.matrix.org/v1.19/)

This index covers the HTTP endpoints listed in the Client-Server,
Server-Server, Application Service, Identity Service, and Push Gateway
API pages.

**Deprecation status** reflects explicit `(deprecated)` markers in the
v1.19 endpoint index. `Not marked deprecated` does not mean an endpoint
is guaranteed to remain forever; consult the linked specification for
its lifecycle and support requirements. `Legacy` is used in remarks
where relevant.

  ---------------------------------------------------------------------------------------------------------------------------------------------
  Endpoint                                                                                       Action Deprecation      Remark
                                                                                                        status           
  ------------------------------------------------------------------------------- --------------------- ---------------- ----------------------
  `/.well-known/matrix/client`                                                                      GET Not marked       Server discovery /
                                                                                                        deprecated       client well-known
                                                                                                                         configuration

  `/.well-known/matrix/support`                                                                     GET Not marked       Server support
                                                                                                        deprecated       information

  `/.well-known/matrix/policy_server`                                                               GET Not marked       Policy server
                                                                                                        deprecated       discovery

  `/_matrix/client/versions`                                                                        GET Not marked       API version discovery
                                                                                                        deprecated       

  `/_matrix/client/v1/register/m.login.registration_token/validity`                                 GET Not marked       Registration token
                                                                                                        deprecated       validation

  `/_matrix/client/v3/login`                                                                        GET Not marked       Login flows
                                                                                                        deprecated       

  `/_matrix/client/v3/login`                                                                       POST Not marked       Authentication / login
                                                                                                        deprecated       

  `/_matrix/client/v1/login/get_token`                                                             POST Not marked       Login token issuance
                                                                                                        deprecated       

  `/_matrix/client/v3/refresh`                                                                     POST Not marked       Refresh access token
                                                                                                        deprecated       

  `/_matrix/client/v3/logout`                                                                      POST Not marked       Logout current device
                                                                                                        deprecated       

  `/_matrix/client/v3/logout/all`                                                                  POST Not marked       Logout all devices
                                                                                                        deprecated       

  `/_matrix/client/v3/register`                                                                    POST Not marked       Account registration
                                                                                                        deprecated       

  `/_matrix/client/v3/register/available`                                                           GET Not marked       Username availability
                                                                                                        deprecated       

  `/_matrix/client/v3/register/email/requestToken`                                                 POST Not marked       Email registration
                                                                                                        deprecated       token

  `/_matrix/client/v3/register/msisdn/requestToken`                                                POST Not marked       Phone registration
                                                                                                        deprecated       token

  `/_matrix/client/v3/account/password`                                                            POST Not marked       Change account
                                                                                                        deprecated       password

  `/_matrix/client/v3/account/password/email/requestToken`                                         POST Not marked       Password reset via
                                                                                                        deprecated       email

  `/_matrix/client/v3/account/password/msisdn/requestToken`                                        POST Not marked       Password reset via
                                                                                                        deprecated       phone

  `/_matrix/client/v3/account/deactivate`                                                          POST Not marked       Account deactivation
                                                                                                        deprecated       

  `/_matrix/client/v1/auth_metadata`                                                                GET Not marked       Authentication
                                                                                                        deprecated       metadata

  `/_matrix/client/v3/account/3pid`                                                                 GET Not marked       List third-party
                                                                                                        deprecated       identifiers

  `/_matrix/client/v3/account/3pid`                                                                POST Deprecated       Legacy third-party
                                                                                                                         identifier management

  `/_matrix/client/v3/account/3pid/add`                                                            POST Not marked       Add third-party
                                                                                                        deprecated       identifier

  `/_matrix/client/v3/account/3pid/bind`                                                           POST Not marked       Bind third-party
                                                                                                        deprecated       identifier

  `/_matrix/client/v3/account/3pid/delete`                                                         POST Not marked       Delete third-party
                                                                                                        deprecated       identifier

  `/_matrix/client/v3/account/3pid/email/requestToken`                                             POST Not marked       Email validation token
                                                                                                        deprecated       

  `/_matrix/client/v3/account/3pid/msisdn/requestToken`                                            POST Not marked       Phone validation token
                                                                                                        deprecated       

  `/_matrix/client/v3/account/3pid/unbind`                                                         POST Not marked       Unbind third-party
                                                                                                        deprecated       identifier

  `/_matrix/client/v3/account/whoami`                                                               GET Not marked       Current authenticated
                                                                                                        deprecated       account

  `/_matrix/client/v3/capabilities`                                                                 GET Not marked       Homeserver
                                                                                                        deprecated       capabilities

  `/_matrix/client/v3/user/{userId}/filter`                                                        POST Not marked       Create sync filter
                                                                                                        deprecated       

  `/_matrix/client/v3/user/{userId}/filter/{filterId}`                                              GET Not marked       Retrieve sync filter
                                                                                                        deprecated       

  `/_matrix/client/v3/sync`                                                                         GET Not marked       Client synchronisation
                                                                                                        deprecated       / event stream

  `/_matrix/client/v3/events`                                                                       GET Deprecated       Legacy event stream

  `/_matrix/client/v3/events/{eventId}`                                                             GET Deprecated       Legacy event retrieval

  `/_matrix/client/v3/initialSync`                                                                  GET Deprecated       Legacy initial sync

  `/_matrix/client/v3/rooms/{roomId}/event/{eventId}`                                               GET Not marked       Retrieve room event
                                                                                                        deprecated       

  `/_matrix/client/v3/rooms/{roomId}/joined_members`                                                GET Not marked       Joined room members
                                                                                                        deprecated       

  `/_matrix/client/v3/rooms/{roomId}/members`                                                       GET Not marked       Room membership
                                                                                                        deprecated       

  `/_matrix/client/v3/rooms/{roomId}/state`                                                         GET Not marked       Room state
                                                                                                        deprecated       

  `/_matrix/client/v3/rooms/{roomId}/state/{eventType}/{stateKey}`                                  GET Not marked       Get specific state
                                                                                                        deprecated       event

  `/_matrix/client/v3/rooms/{roomId}/messages`                                                      GET Not marked       Room message history /
                                                                                                        deprecated       pagination

  `/_matrix/client/v1/rooms/{roomId}/timestamp_to_event`                                            GET Not marked       Timestamp-to-event
                                                                                                        deprecated       lookup

  `/_matrix/client/v3/rooms/{roomId}/initialSync`                                                   GET Not marked       Legacy room initial
                                                                                                        deprecated       sync

  `/_matrix/client/v3/rooms/{roomId}/state/{eventType}/{stateKey}`                                  PUT Not marked       Set room state
                                                                                                        deprecated       

  `/_matrix/client/v3/rooms/{roomId}/send/{eventType}/{txnId}`                                      PUT Not marked       Send room event /
                                                                                                        deprecated       message

  `/_matrix/client/v3/rooms/{roomId}/redact/{eventId}/{txnId}`                                      PUT Not marked       Redact room event
                                                                                                        deprecated       

  `/_matrix/client/v1/rooms/{roomId}/relations/{eventId}`                                           GET Not marked       Event relations
                                                                                                        deprecated       

  `/_matrix/client/v1/rooms/{roomId}/relations/{eventId}/{relType}`                                 GET Not marked       Relations by relation
                                                                                                        deprecated       type

  `/_matrix/client/v1/rooms/{roomId}/relations/{eventId}/{relType}/{eventType}`                     GET Not marked       Relations filtered by
                                                                                                        deprecated       event type

  `/_matrix/client/v3/createRoom`                                                                  POST Not marked       Room creation
                                                                                                        deprecated       

  `/_matrix/client/v3/directory/room/{roomAlias}`                                                   GET Not marked       Room alias resolution
                                                                                                        deprecated       

  `/_matrix/client/v3/directory/room/{roomAlias}`                                                   PUT Not marked       Create room alias
                                                                                                        deprecated       

  `/_matrix/client/v3/directory/room/{roomAlias}`                                                DELETE Not marked       Delete room alias
                                                                                                        deprecated       

  `/_matrix/client/v3/rooms/{roomId}/aliases`                                                       GET Not marked       Local room aliases
                                                                                                        deprecated       

  `/_matrix/client/v3/joined_rooms`                                                                 GET Not marked       List joined rooms
                                                                                                        deprecated       

  `/_matrix/client/v3/rooms/{roomId}/invite`                                                       POST Not marked       Invite user to room
                                                                                                        deprecated       

  `/_matrix/client/v3/join/{roomIdOrAlias}`                                                        POST Not marked       Join room by ID or
                                                                                                        deprecated       alias

  `/_matrix/client/v3/rooms/{roomId}/join`                                                         POST Not marked       Join room
                                                                                                        deprecated       

  `/_matrix/client/v3/knock/{roomIdOrAlias}`                                                       POST Not marked       Knock on room
                                                                                                        deprecated       

  `/_matrix/client/v3/rooms/{roomId}/forget`                                                       POST Not marked       Forget room
                                                                                                        deprecated       

  `/_matrix/client/v3/rooms/{roomId}/leave`                                                        POST Not marked       Leave room
                                                                                                        deprecated       

  `/_matrix/client/v3/rooms/{roomId}/kick`                                                         POST Not marked       Kick room member
                                                                                                        deprecated       

  `/_matrix/client/v3/rooms/{roomId}/ban`                                                          POST Not marked       Ban room member
                                                                                                        deprecated       

  `/_matrix/client/v3/rooms/{roomId}/unban`                                                        POST Not marked       Unban room member
                                                                                                        deprecated       

  `/_matrix/client/v3/directory/list/room/{roomId}`                                                 GET Not marked       Published room
                                                                                                        deprecated       directory visibility

  `/_matrix/client/v3/directory/list/room/{roomId}`                                                 PUT Not marked       Publish/unpublish room
                                                                                                        deprecated       in directory

  `/_matrix/client/v3/publicRooms`                                                                  GET Not marked       Public room directory
                                                                                                        deprecated       

  `/_matrix/client/v3/publicRooms`                                                                 POST Not marked       Search public rooms
                                                                                                        deprecated       

  `/_matrix/client/v1/room_summary/{roomIdOrAlias}`                                                 GET Not marked       Room summary
                                                                                                        deprecated       

  `/_matrix/client/v3/user_directory/search`                                                       POST Not marked       User directory search
                                                                                                        deprecated       

  `/_matrix/client/v3/profile/{userId}`                                                             GET Not marked       User profile
                                                                                                        deprecated       

  `/_matrix/client/v3/profile/{userId}/{keyName}`                                                   GET Not marked       Get profile field
                                                                                                        deprecated       

  `/_matrix/client/v3/profile/{userId}/{keyName}`                                                   PUT Not marked       Set profile field
                                                                                                        deprecated       

  `/_matrix/client/v3/profile/{userId}/{keyName}`                                                DELETE Not marked       Delete profile field
                                                                                                        deprecated       

  `/_matrix/client/v3/user/{userId}/account_data/{type}`                                            GET Not marked       Global user account
                                                                                                        deprecated       data

  `/_matrix/client/v3/user/{userId}/account_data/{type}`                                            PUT Not marked       Set global user
                                                                                                        deprecated       account data

  `/_matrix/client/v3/user/{userId}/rooms/{roomId}/account_data/{type}`                             GET Not marked       Room-scoped account
                                                                                                        deprecated       data

  `/_matrix/client/v3/user/{userId}/rooms/{roomId}/account_data/{type}`                             PUT Not marked       Set room-scoped
                                                                                                        deprecated       account data

  `/_matrix/client/v1/media/config`                                                                 GET Not marked       Media repository
                                                                                                        deprecated       configuration

  `/_matrix/client/v1/media/download/{serverName}/{mediaId}`                                        GET Not marked       Media download
                                                                                                        deprecated       

  `/_matrix/client/v1/media/download/{serverName}/{mediaId}/{fileName}`                             GET Not marked       Media download with
                                                                                                        deprecated       filename

  `/_matrix/client/v1/media/preview_url`                                                            GET Not marked       URL preview metadata
                                                                                                        deprecated       

  `/_matrix/client/v1/media/thumbnail/{serverName}/{mediaId}`                                       GET Not marked       Media thumbnail
                                                                                                        deprecated       

  `/_matrix/media/v1/create`                                                                       POST Not marked       Create media content
                                                                                                        deprecated       resource

  `/_matrix/media/v3/config`                                                                        GET Deprecated       Legacy media
                                                                                                                         configuration

  `/_matrix/media/v3/download/{serverName}/{mediaId}`                                               GET Deprecated       Legacy media download

  `/_matrix/media/v3/download/{serverName}/{mediaId}/{fileName}`                                    GET Deprecated       Legacy media download
                                                                                                                         with filename

  `/_matrix/media/v3/preview_url`                                                                   GET Deprecated       Legacy URL preview

  `/_matrix/media/v3/thumbnail/{serverName}/{mediaId}`                                              GET Deprecated       Legacy media thumbnail

  `/_matrix/media/v3/upload`                                                                       POST Not marked       Legacy media upload
                                                                                                        deprecated       

  `/_matrix/media/v3/upload/{serverName}/{mediaId}`                                                 PUT Not marked       Legacy media upload
                                                                                                        deprecated       

  `/_matrix/client/v3/delete_devices`                                                              POST Not marked       Delete devices
                                                                                                        deprecated       

  `/_matrix/client/v3/devices`                                                                      GET Not marked       List devices
                                                                                                        deprecated       

  `/_matrix/client/v3/devices/{deviceId}`                                                           GET Not marked       Get device
                                                                                                        deprecated       

  `/_matrix/client/v3/devices/{deviceId}`                                                           PUT Not marked       Update device display
                                                                                                        deprecated       name

  `/_matrix/client/v3/devices/{deviceId}`                                                        DELETE Not marked       Delete device
                                                                                                        deprecated       

  `/_matrix/client/v3/keys/device_signing/upload`                                                  POST Not marked       E2EE device/user
                                                                                                        deprecated       signing keys

  `/_matrix/client/v3/keys/signatures/upload`                                                      POST Not marked       E2EE signature upload
                                                                                                        deprecated       

  `/_matrix/client/v3/room_keys/keys`                                                               GET Not marked       Backup room keys
                                                                                                        deprecated       

  `/_matrix/client/v3/room_keys/keys`                                                               PUT Not marked       Store backup room keys
                                                                                                        deprecated       

  `/_matrix/client/v3/room_keys/keys`                                                            DELETE Not marked       Delete backup room
                                                                                                        deprecated       keys

  `/_matrix/client/v3/room_keys/keys/{roomId}`                                                      GET Not marked       Get room key backup by
                                                                                                        deprecated       room

  `/_matrix/client/v3/room_keys/keys/{roomId}`                                                      PUT Not marked       Store room key backup
                                                                                                        deprecated       by room

  `/_matrix/client/v3/room_keys/keys/{roomId}`                                                   DELETE Not marked       Delete room key backup
                                                                                                        deprecated       by room

  `/_matrix/client/v3/room_keys/keys/{roomId}/{sessionId}`                                          GET Not marked       Get individual
                                                                                                        deprecated       backed-up session key

  `/_matrix/client/v3/room_keys/keys/{roomId}/{sessionId}`                                          PUT Not marked       Store individual
                                                                                                        deprecated       backed-up session key

  `/_matrix/client/v3/room_keys/keys/{roomId}/{sessionId}`                                       DELETE Not marked       Delete individual
                                                                                                        deprecated       backed-up session key

  `/_matrix/client/v3/room_keys/version`                                                            GET Not marked       Get key backup version
                                                                                                        deprecated       

  `/_matrix/client/v3/room_keys/version`                                                           POST Not marked       Create key backup
                                                                                                        deprecated       version

  `/_matrix/client/v3/room_keys/version/{version}`                                                  GET Not marked       Get specific key
                                                                                                        deprecated       backup version

  `/_matrix/client/v3/room_keys/version/{version}`                                                  PUT Not marked       Update key backup
                                                                                                        deprecated       version

  `/_matrix/client/v3/room_keys/version/{version}`                                               DELETE Not marked       Delete key backup
                                                                                                        deprecated       version

  `/_matrix/client/v3/keys/changes`                                                                 GET Not marked       E2EE device list
                                                                                                        deprecated       changes

  `/_matrix/client/v3/keys/claim`                                                                  POST Not marked       Claim one-time keys
                                                                                                        deprecated       

  `/_matrix/client/v3/keys/query`                                                                  POST Not marked       Query device keys
                                                                                                        deprecated       

  `/_matrix/client/v3/keys/upload`                                                                 POST Not marked       Upload device keys
                                                                                                        deprecated       

  `/_matrix/client/v3/rooms/{roomId}/context/{eventId}`                                             GET Not marked       Event context
                                                                                                        deprecated       

  `/_matrix/client/v1/mutual_rooms`                                                                 GET Not marked       Mutual rooms
                                                                                                        deprecated       

  `/_matrix/client/v3/user/{userId}/openid/request_token`                                          POST Not marked       OpenID token
                                                                                                        deprecated       

  `/_matrix/client/v3/presence/{userId}/status`                                                     GET Not marked       Get user presence
                                                                                                        deprecated       

  `/_matrix/client/v3/presence/{userId}/status`                                                     PUT Not marked       Set user presence
                                                                                                        deprecated       

  `/_matrix/client/v3/pushrules/`                                                                   GET Not marked       Push rules
                                                                                                        deprecated       

  `/_matrix/client/v3/pushrules/global/`                                                            GET Not marked       Global push rules
                                                                                                        deprecated       

  `/_matrix/client/v3/pushrules/global/{kind}/{ruleId}`                                             GET Not marked       Get push rule
                                                                                                        deprecated       

  `/_matrix/client/v3/pushrules/global/{kind}/{ruleId}`                                             PUT Not marked       Create/update push
                                                                                                        deprecated       rule

  `/_matrix/client/v3/pushrules/global/{kind}/{ruleId}`                                          DELETE Not marked       Delete push rule
                                                                                                        deprecated       

  `/_matrix/client/v3/pushrules/global/{kind}/{ruleId}/actions`                                     GET Not marked       Get push rule actions
                                                                                                        deprecated       

  `/_matrix/client/v3/pushrules/global/{kind}/{ruleId}/actions`                                     PUT Not marked       Set push rule actions
                                                                                                        deprecated       

  `/_matrix/client/v3/pushrules/global/{kind}/{ruleId}/enabled`                                     GET Not marked       Get push rule enabled
                                                                                                        deprecated       state

  `/_matrix/client/v3/pushrules/global/{kind}/{ruleId}/enabled`                                     PUT Not marked       Enable/disable push
                                                                                                        deprecated       rule

  `/_matrix/client/v3/pushers`                                                                      GET Not marked       List pushers
                                                                                                        deprecated       

  `/_matrix/client/v3/pushers/set`                                                                 POST Not marked       Create/update/delete
                                                                                                        deprecated       pusher

  `/_matrix/client/v3/notifications`                                                                GET Not marked       Notification list
                                                                                                        deprecated       

  `/_matrix/client/v3/rooms/{roomId}/read_markers`                                                 POST Not marked       Read and unread
                                                                                                        deprecated       markers

  `/_matrix/client/v3/rooms/{roomId}/receipt/{receiptType}/{eventId}`                              POST Not marked       Read receipts
                                                                                                        deprecated       

  `/_matrix/client/v3/rooms/{roomId}/report`                                                       POST Not marked       Report room content
                                                                                                        deprecated       

  `/_matrix/client/v3/rooms/{roomId}/report/{eventId}`                                             POST Not marked       Report a specific
                                                                                                        deprecated       event

  `/_matrix/client/v3/users/{userId}/report`                                                       POST Not marked       Report user
                                                                                                        deprecated       

  `/_matrix/client/v3/events`                                                                       GET Not marked       Room preview event
                                                                                                        deprecated       access

  `/_matrix/client/v3/user/{userId}/rooms/{roomId}/tags`                                            GET Not marked       Room tagging / list
                                                                                                        deprecated       tags

  `/_matrix/client/v3/user/{userId}/rooms/{roomId}/tags/{tag}`                                      PUT Not marked       Room tagging / add or
                                                                                                        deprecated       update tag

  `/_matrix/client/v3/user/{userId}/rooms/{roomId}/tags/{tag}`                                   DELETE Not marked       Room tagging / remove
                                                                                                        deprecated       tag

  `/_matrix/client/v3/rooms/{roomId}/upgrade`                                                      POST Not marked       Room upgrade
                                                                                                        deprecated       

  `/_matrix/client/v3/sendToDevice/{eventType}/{txnId}`                                             PUT Not marked       Send-to-device
                                                                                                        deprecated       messaging

  `/_matrix/client/v1/admin/lock/{userId}`                                                          GET Not marked       Account locking status
                                                                                                        deprecated       (admin)

  `/_matrix/client/v1/admin/lock/{userId}`                                                          PUT Not marked       Lock/unlock account
                                                                                                        deprecated       (admin)

  `/_matrix/client/v1/admin/suspend/{userId}`                                                       GET Not marked       Account suspension
                                                                                                        deprecated       status (admin)

  `/_matrix/client/v1/admin/suspend/{userId}`                                                       PUT Not marked       Suspend/unsuspend
                                                                                                        deprecated       account (admin)

  `/_matrix/client/v3/admin/whois/{userId}`                                                         GET Not marked       User session/device
                                                                                                        deprecated       information (admin)

  `/_matrix/client/v3/search`                                                                      POST Not marked       Server-side event
                                                                                                        deprecated       search

  `/_matrix/client/v1/rooms/{roomId}/hierarchy`                                                     GET Not marked       Spaces / room
                                                                                                        deprecated       hierarchy

  `/_matrix/client/v3/login/sso/redirect`                                                           GET Not marked       SSO login redirect
                                                                                                        deprecated       

  `/_matrix/client/v3/login/sso/redirect/{idpId}`                                                   GET Not marked       SSO redirect via
                                                                                                        deprecated       identity provider

  `/_matrix/client/v3/thirdparty/location`                                                          GET Not marked       Third-party network
                                                                                                        deprecated       location lookup

  `/_matrix/client/v3/thirdparty/location/{protocol}`                                               GET Not marked       Third-party location
                                                                                                        deprecated       lookup by protocol

  `/_matrix/client/v3/thirdparty/protocol/{protocol}`                                               GET Not marked       Third-party protocol
                                                                                                        deprecated       details

  `/_matrix/client/v3/thirdparty/protocols`                                                         GET Not marked       Third-party protocol
                                                                                                        deprecated       listing

  `/_matrix/client/v3/thirdparty/user`                                                              GET Not marked       Third-party user
                                                                                                        deprecated       lookup

  `/_matrix/client/v3/thirdparty/user/{protocol}`                                                   GET Not marked       Third-party user
                                                                                                        deprecated       lookup by protocol

  `/_matrix/client/v1/rooms/{roomId}/threads`                                                       GET Not marked       Thread listing
                                                                                                        deprecated       

  `/_matrix/client/v3/rooms/{roomId}/typing/{userId}`                                               PUT Not marked       Typing notifications
                                                                                                        deprecated       

  `/_matrix/client/v3/voip/turnServer`                                                              GET Not marked       VoIP TURN server
                                                                                                        deprecated       configuration

  `/.well-known/matrix/server`                                                                      GET Not marked       Federation server
                                                                                                        deprecated       discovery

  `/_matrix/federation/v1/version`                                                                  GET Not marked       Federation version
                                                                                                        deprecated       

  `/_matrix/key/v2/server`                                                                          GET Not marked       Publish signing keys
                                                                                                        deprecated       

  `/_matrix/key/v2/query`                                                                          POST Not marked       Query signing keys
                                                                                                        deprecated       

  `/_matrix/key/v2/query/{serverName}`                                                              GET Not marked       Query signing keys for
                                                                                                        deprecated       server

  `/_matrix/federation/v1/send/{txnId}`                                                             PUT Not marked       Federation
                                                                                                        deprecated       transactions (PDUs and
                                                                                                                         EDUs)

  `/_matrix/federation/v1/event_auth/{roomId}/{eventId}`                                            GET Not marked       Event authorisation
                                                                                                        deprecated       chain

  `/_matrix/federation/v1/backfill/{roomId}`                                                        GET Not marked       Backfill room history
                                                                                                        deprecated       

  `/_matrix/federation/v1/get_missing_events/{roomId}`                                             POST Not marked       Retrieve missing
                                                                                                        deprecated       events

  `/_matrix/federation/v1/event/{eventId}`                                                          GET Not marked       Retrieve event
                                                                                                        deprecated       

  `/_matrix/federation/v1/state/{roomId}`                                                           GET Not marked       Room state
                                                                                                        deprecated       

  `/_matrix/federation/v1/state_ids/{roomId}`                                                       GET Not marked       Room state event IDs
                                                                                                        deprecated       

  `/_matrix/federation/v1/timestamp_to_event/{roomId}`                                              GET Not marked       Timestamp-to-event
                                                                                                        deprecated       lookup

  `/_matrix/federation/v1/make_join/{roomId}/{userId}`                                              GET Not marked       Room join handshake
                                                                                                        deprecated       

  `/_matrix/federation/v2/send_join/{roomId}/{eventId}`                                             PUT Not marked       Complete room join
                                                                                                        deprecated       

  `/_matrix/federation/v1/make_knock/{roomId}/{userId}`                                             GET Not marked       Room knock handshake
                                                                                                        deprecated       

  `/_matrix/federation/v1/send_knock/{roomId}/{eventId}`                                            PUT Not marked       Complete room knock
                                                                                                        deprecated       

  `/_matrix/federation/v1/invite/{roomId}/{eventId}`                                                PUT Not marked       Federated room invite
                                                                                                        deprecated       

  `/_matrix/federation/v2/invite/{roomId}/{eventId}`                                                PUT Not marked       Federated room invite
                                                                                                        deprecated       (v2)

  `/_matrix/federation/v1/make_leave/{roomId}/{userId}`                                             GET Not marked       Room leave handshake
                                                                                                        deprecated       

  `/_matrix/federation/v2/send_leave/{roomId}/{eventId}`                                            PUT Not marked       Complete room leave
                                                                                                        deprecated       

  `/_matrix/federation/v1/3pid/onbind`                                                              PUT Not marked       Third-party identifier
                                                                                                        deprecated       binding notification

  `/_matrix/federation/v1/exchange_third_party_invite/{roomId}`                                     PUT Not marked       Third-party invite
                                                                                                        deprecated       exchange

  `/_matrix/federation/v1/publicRooms`                                                              GET Not marked       Federated public room
                                                                                                        deprecated       directory

  `/_matrix/federation/v1/publicRooms`                                                             POST Not marked       Federated public room
                                                                                                        deprecated       directory search

  `/_matrix/federation/v1/hierarchy/{roomId}`                                                       GET Not marked       Federated space
                                                                                                        deprecated       hierarchy

  `/_matrix/federation/v1/query/directory`                                                          GET Not marked       Room alias directory
                                                                                                        deprecated       query

  `/_matrix/federation/v1/query/profile`                                                            GET Not marked       User profile query
                                                                                                        deprecated       

  `/_matrix/federation/v1/query/{queryType}`                                                        GET Not marked       Federation query API
                                                                                                        deprecated       

  `/_matrix/federation/v1/openid/userinfo`                                                          GET Not marked       OpenID user
                                                                                                        deprecated       information

  `/_matrix/federation/v1/user/devices/{userId}`                                                    GET Not marked       Federated device list
                                                                                                        deprecated       

  `/_matrix/federation/v1/user/keys/claim`                                                         POST Not marked       Claim device one-time
                                                                                                        deprecated       keys

  `/_matrix/federation/v1/user/keys/query`                                                         POST Not marked       Query device keys
                                                                                                        deprecated       

  `/_matrix/federation/v1/media/download/{mediaId}`                                                 GET Not marked       Federated media
                                                                                                        deprecated       download

  `/_matrix/federation/v1/media/thumbnail/{mediaId}`                                                GET Not marked       Federated media
                                                                                                        deprecated       thumbnail

  `/_matrix/policy/v1/sign`                                                                        POST Not marked       Policy server event
                                                                                                        deprecated       signing

  `/_matrix/app/v1/transactions/{txnId}`                                                            PUT Not marked       Application Service /
                                                                                                        deprecated       receive event
                                                                                                                         transactions

  `/_matrix/app/v1/ping`                                                                           POST Not marked       Application Service
                                                                                                        deprecated       health ping

  `/_matrix/app/v1/users/{userId}`                                                                  GET Not marked       Application Service
                                                                                                        deprecated       user query

  `/_matrix/app/v1/rooms/{roomAlias}`                                                               GET Not marked       Application Service
                                                                                                        deprecated       room alias query

  `/_matrix/app/v1/thirdparty/location`                                                             GET Not marked       Third-party location
                                                                                                        deprecated       query

  `/_matrix/app/v1/thirdparty/location/{protocol}`                                                  GET Not marked       Third-party location
                                                                                                        deprecated       query by protocol

  `/_matrix/app/v1/thirdparty/protocol/{protocol}`                                                  GET Not marked       Third-party protocol
                                                                                                        deprecated       query

  `/_matrix/app/v1/thirdparty/user`                                                                 GET Not marked       Third-party user query
                                                                                                        deprecated       

  `/_matrix/app/v1/thirdparty/user/{protocol}`                                                      GET Not marked       Third-party user query
                                                                                                        deprecated       by protocol

  `/_matrix/client/v1/appservice/{appserviceId}/ping`                                              POST Not marked       Application Service
                                                                                                        deprecated       ping (client-server
                                                                                                                         extension)

  `/_matrix/client/v3/directory/list/appservice/{networkId}/{roomId}`                               PUT Not marked       Application Service
                                                                                                        deprecated       published room
                                                                                                                         directory

  `/_matrix/identity/versions`                                                                      GET Not marked       Identity API version
                                                                                                        deprecated       discovery

  `/_matrix/identity/v2/account`                                                                    GET Not marked       Identity account
                                                                                                        deprecated       status

  `/_matrix/identity/v2/account/logout`                                                            POST Not marked       Identity account
                                                                                                        deprecated       logout

  `/_matrix/identity/v2/account/register`                                                          POST Not marked       Identity account
                                                                                                        deprecated       registration

  `/_matrix/identity/v2/terms`                                                                      GET Not marked       Terms of service
                                                                                                        deprecated       retrieval

  `/_matrix/identity/v2/terms`                                                                     POST Not marked       Terms of service
                                                                                                        deprecated       acceptance

  `/_matrix/identity/v2`                                                                            GET Not marked       Identity server
                                                                                                        deprecated       information

  `/_matrix/identity/v2/pubkey/ephemeral/isvalid`                                                   GET Not marked       Validate ephemeral
                                                                                                        deprecated       public key

  `/_matrix/identity/v2/pubkey/isvalid`                                                             GET Not marked       Validate public key
                                                                                                        deprecated       

  `/_matrix/identity/v2/pubkey/{keyId}`                                                             GET Not marked       Retrieve public key
                                                                                                        deprecated       

  `/_matrix/identity/v2/hash_details`                                                               GET Not marked       Lookup hashing
                                                                                                        deprecated       algorithms/details

  `/_matrix/identity/v2/lookup`                                                                    POST Not marked       Third-party identifier
                                                                                                        deprecated       association lookup

  `/_matrix/identity/v2/validate/email/requestToken`                                               POST Not marked       Request email
                                                                                                        deprecated       validation token

  `/_matrix/identity/v2/validate/email/submitToken`                                                 GET Not marked       Submit email
                                                                                                        deprecated       validation token (GET
                                                                                                                         callback)

  `/_matrix/identity/v2/validate/email/submitToken`                                                POST Not marked       Submit email
                                                                                                        deprecated       validation token

  `/_matrix/identity/v2/validate/msisdn/requestToken`                                              POST Not marked       Request phone
                                                                                                        deprecated       validation token

  `/_matrix/identity/v2/validate/msisdn/submitToken`                                                GET Not marked       Submit phone
                                                                                                        deprecated       validation token (GET
                                                                                                                         callback)

  `/_matrix/identity/v2/validate/msisdn/submitToken`                                               POST Not marked       Submit phone
                                                                                                        deprecated       validation token

  `/_matrix/identity/v2/3pid/bind`                                                                 POST Not marked       Bind third-party
                                                                                                        deprecated       identifier

  `/_matrix/identity/v2/3pid/getValidated3pid`                                                      GET Not marked       Get validated
                                                                                                        deprecated       third-party identifier

  `/_matrix/identity/v2/3pid/unbind`                                                               POST Not marked       Unbind third-party
                                                                                                        deprecated       identifier

  `/_matrix/identity/v2/store-invite`                                                              POST Not marked       Store third-party
                                                                                                        deprecated       invite

  `/_matrix/identity/v2/sign-ed25519`                                                              POST Not marked       Sign invitation with
                                                                                                        deprecated       Ed25519

  `/_matrix/push/v1/notify`                                                                        POST Not marked       Push Gateway / receive
                                                                                                        deprecated       notification payload
  ---------------------------------------------------------------------------------------------------------------------------------------------

## Notes

-   The table includes the endpoint entries from the v1.19 API endpoint
    indexes, including endpoints that are explicitly marked deprecated
    there.
-   HTTP `OPTIONS` is not listed as a separate business endpoint: the
    Client-Server specification states that it is supported for CORS
    preflight on all endpoints and does not execute endpoint logic.
-   Some older media routes are retained in the specification for
    compatibility. Check the endpoint's own section for migration
    guidance and exact support expectations.
-   The "Remark" column is a concise functional grouping/summary, not a
    replacement for the normative endpoint description, authentication
    requirements, request schema, or response schema.
-   The Client-Server API has some nuanced routes whose semantics
    overlap (for example, legacy room preview/event access). Refer to
    the source section before implementing.

## Source pages

-   [Client-Server
    API](https://spec.matrix.org/v1.19/client-server-api/)
-   [Server-Server
    API](https://spec.matrix.org/v1.19/server-server-api/)
-   [Application Service
    API](https://spec.matrix.org/v1.19/application-service-api/)
-   [Identity Service
    API](https://spec.matrix.org/v1.19/identity-service-api/)
-   [Push Gateway API](https://spec.matrix.org/v1.19/push-gateway-api/)

