# README - ELUVIO

Eluvio code is based on `v1.10.19`     (2024-08-14)

Using branch `elv-release-1.10.19`


### General Process

Create a branch based on either master, release branch or tag:

  - elv-master  (from `master`)
  - elv-release-1.10.19 (from tag `v1.10.19`)

All Eluvio changes are pushed to this branch.

### Configuration Changes

#### Clique Changes

There are a few changes made to the clique config, mainly designed to make it more workable as a production consensus engine.
The actual clique consensus engine is only designed for use in test networks.

All changes that made are best-effort on the part of the validators, in the sense that the validators will not reject blocks that are made by validators not following those rules.
As such, it is reasonable to gradually apply (genesis) config changes across the network, and that there is no need for fork-related operational procedures, e.g. applying changes at a specific block.

In order to understand the changes, it is helpful to understand a bit about how the clique config works:

- Every validator is part of the 'clique', and _any_ block that is created by it is considered a valid block
- There is a concept of a block being "in-turn", which means that block `n` should ideally be created by validator `n % len(validators)`. This is implemented by giving in-turn blocks a higher 'mining weight' than others.
- If a block time is set, a validator will wait until the block time has passed before trying to publish another block.

The things that we made configurable are:

##### `signer_random_delay`

This scales a random delay in milliseconds that signers wait before making an out-of-turn block, when there is a block time set. The validator will wait anywhere between 0 and the full delay before producing a block out-of-turn.

##### `out_of_turn_min_delay`

This sets a minimum delay in milliseconds that signers will wait before making an out-of-turn block when there is a block time set.

##### Example

Consider a block time of 3 seconds, and 3 validators `A`, `B`, and `C`, and a random delay of `500ms`, and a minimum delay of `100ms`.
If `A` produced the last block at time `15.00s`, the following scenario could happen if `B` is down:

- `15.00s-18.00s`: `A`, `B`, and `C` are constructing blocks, but not publishing/mining them, as it is before the minimum block time.
- `18.00s`: If `B` has a block to create, it will mine and publish it. `A` and `C` will decide their random delay between 0 and `1000ms` (the random delay scales on the number of validators). Suppose `B` is offline and does not publish a block and `A` gets a delay of `600ms`, and `C` a delay of `200ms`. `C` will wait `200ms + 100ms`, and `A` will wait `600ms + 100ms`.
- `18.30s`: `C` mines and publishes its block.
- Slightly after `18.30s`, suppose 100ms for transmission: `A` receives the block from `C`, and restarts its block timer.

and if `B` is up:

- `15.00s-18.00s`: `A`, `B`, and `C` are constructing blocks, but not publishing/mining them, as it is before the minimum block time.
- `18.00s`: `B` mines and publishes a block. `A` and `C` will decide their random delay between 0 and `1000ms` (the random delay scales on the number of validators). Suppose `B` is offline and does not publish a block and `A` gets a delay of `600ms`, and `C` a delay of `200ms`. `C` will wait `200ms + 100ms`, and `A` will wait `600ms + 100ms`.
- `18.10s`: `A` and `C` hear about `B`'s block.