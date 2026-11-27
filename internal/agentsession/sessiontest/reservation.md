# Registry reservation and ataniq.session-ref/1

The registry pins the private `packSessionRef` in `TestPackSessionRefShapeIsReserved`.
The table follows its declaration order. The reservation has no wire encoding.

| Reservation member | Type | /1 member | Source or interpretation in S1 |
| --- | --- | --- | --- |
| authority | string | authority | Admitted enrollment's opaque authority |
| storeIncarnation | string | store_incarnation | Same enrollment's incarnation |
| project | string | project | Launched project |
| homeGeneration | uint64 | home_generation | 1 |
| role | string | role | Launched role |
| conversation | string | conversation | Launch intent ID |
| roleEpoch | uint64 | launch_epoch | Launch intent epoch |
| bindingGeneration | uint64 | binding_generation | 1 |
| pack | PackRef | pack, pack_digest | Name and manifest digest correspond to the launched identity and its declaration digest. The holder hello supplies both. |
| signerGeneration | uint64 | none | Built-in packs have no signer generation. |
| profileDigest | string | none | The registry retains the requested launch-profile digest. |
| transport | string | transport | S1 uses `terminal`. The reservation's `headless` corresponds to `protocol`. |
| custody | string | custody | S1 uses `local`. |

Three /1 members have no reservation field. The intent ID supplies `launch`,
`conversation_incarnation` is 1, and the sandbox profile for this start supplies
`sandbox_digest`.
