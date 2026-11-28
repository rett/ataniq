The Qargi lead recorded the following SHA-256 values for the standard output of
`qargi provider show NAME --json` (runtime crosswalk §3). Each definition call
exited 0 with empty standard error. The family-name calls for `claude` and
`openai` exited 1 with empty standard output.

| NAME | Tag 0.3.5 | Develop at e261aaf3 |
| --- | --- | --- |
| claude-code | c0534b9cc0cb3e0241324491b07f3782583b4d0dc0e52845cad238c579f2baae | c0534b9cc0cb3e0241324491b07f3782583b4d0dc0e52845cad238c579f2baae |
| claude-platform | de8196d5c93b08a9824cbdcbfa46b121950dae8159299242f982d69b6b7ca550 | de8196d5c93b08a9824cbdcbfa46b121950dae8159299242f982d69b6b7ca550 |
| codex | 795603e14ef6eb46d3e50b0c40049b757986b9b7fd5f79680ae98069f10fda07 | 892762ed92f61e5d595da194485c7f0fef0b7427bee20e7abe9f8398e3e45e2b |

`sources.json` authenticates the three raw Git blobs used as test inputs.
Qargi ships them under the same MIT notice as this repository's LICENSE.

The exit-map cases are test vectors. Historical cases use plain diagnostics.
The structured cases model the contract introduced in 0.3.6, including
synthetic exit 2 and planned exit 77 lines. The adapter success case uses the
`barrier-check` response schema. Outcomes before a status are fixture inputs.
