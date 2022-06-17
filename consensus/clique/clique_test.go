// Copyright 2019 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package clique

import (
	"bytes"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"

	"github.com/holiman/uint256"
)

// This test case is a repro of an annoying bug that took us forever to catch.
// In Clique PoA networks (Rinkeby, Görli, etc), consecutive blocks might have
// the same state root (no block subsidy, empty block). If a node crashes, the
// chain ends up losing the recent state and needs to regenerate it from blocks
// already in the database. The bug was that processing the block *prior* to an
// empty one **also completes** the empty one, ending up in a known-block error.
func TestReimportMirroredState(t *testing.T) {
	// Initialize a Clique chain with a single signer
	var (
		db     = rawdb.NewMemoryDatabase()
		key, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		addr   = crypto.PubkeyToAddress(key.PublicKey)
		engine = New(params.AllCliqueProtocolChanges.Clique, db)
		signer = new(types.HomesteadSigner)
	)
	genspec := &core.Genesis{
		ExtraData: make([]byte, extraVanity+common.AddressLength+extraSeal),
		Alloc: map[common.Address]core.GenesisAccount{
			addr: {Balance: big.NewInt(1)},
		},
	}
	copy(genspec.ExtraData[extraVanity:], addr[:])
	genesis := genspec.MustCommit(db)

	// Generate a batch of blocks, each properly signed
	chain, _ := core.NewBlockChain(db, nil, params.AllCliqueProtocolChanges, engine, vm.Config{}, nil, nil)
	defer chain.Stop()

	blocks, _ := core.GenerateChain(params.AllCliqueProtocolChanges, genesis, engine, db, 3, func(i int, block *core.BlockGen) {
		// The chain maker doesn't have access to a chain, so the difficulty will be
		// lets unset (nil). Set it here to the correct value.
		block.SetDifficulty(diffInTurn)

		// We want to simulate an empty middle block, having the same state as the
		// first one. The last is needs a state change again to force a reorg.
		if i != 1 {
			tx, err := types.SignTx(types.NewTransaction(block.TxNonce(addr), common.Address{0x00}, new(big.Int), params.TxGas, nil, nil), signer, key)
			if err != nil {
				panic(err)
			}
			block.AddTxWithChain(chain, tx)
		}
	})
	for i, block := range blocks {
		header := block.Header()
		if i > 0 {
			header.ParentHash = blocks[i-1].Hash()
		}
		header.Extra = make([]byte, extraVanity+extraSeal)
		header.Difficulty = diffInTurn

		sig, _ := crypto.Sign(SealHash(header).Bytes(), key)
		copy(header.Extra[len(header.Extra)-extraSeal:], sig)
		blocks[i] = block.WithSeal(header)
	}
	// Insert the first two blocks and make sure the chain is valid
	db = rawdb.NewMemoryDatabase()
	genspec.MustCommit(db)

	chain, _ = core.NewBlockChain(db, nil, params.AllCliqueProtocolChanges, engine, vm.Config{}, nil, nil)
	defer chain.Stop()

	if _, err := chain.InsertChain(blocks[:2]); err != nil {
		t.Fatalf("failed to insert initial blocks: %v", err)
	}
	if head := chain.CurrentBlock().NumberU64(); head != 2 {
		t.Fatalf("chain head mismatch: have %d, want %d", head, 2)
	}

	// Simulate a crash by creating a new chain on top of the database, without
	// flushing the dirty states out. Insert the last block, triggering a sidechain
	// reimport.
	chain, _ = core.NewBlockChain(db, nil, params.AllCliqueProtocolChanges, engine, vm.Config{}, nil, nil)
	defer chain.Stop()

	if _, err := chain.InsertChain(blocks[2:]); err != nil {
		t.Fatalf("failed to insert final block: %v", err)
	}
	if head := chain.CurrentBlock().NumberU64(); head != 3 {
		t.Fatalf("chain head mismatch: have %d, want %d", head, 3)
	}
}

func TestClique_EIP3436_Scenario1(t *testing.T) {
	scenarios := []struct {
		// The number of signers (aka validators).
		// These addresses will be randomly generated on demand for each scenario
		// and sorted per status quo Clique spec.
		// They are referenced by their cardinality in this sorted list.
		lenSigners int

		// commonBlocks defines blocks by which validator should sign them.
		// Validators are referenced by their index in the sorted signers list,
		// which sorts by address.
		commonBlocks []int

		// forks defines blocks by which validator should sign them.
		// These forks will be generated and attempted to be imported into
		// the chain. We expect that import should always succeed for all blocks,
		// with this test measuring only if the expected fork head actually gets
		// canonical preference.
		forks [][]int

		// assertions are functions used to assert the expectations of
		// fork heads in chain context against the specification's requirements, eg.
		// make sure that the forks really do have equal total difficulty, or equal block numbers.
		// This way we know that the deciding condition for achieving canonical status
		// really is what we think it is (and not, eg. total difficulty).

		assertions []func(chain *core.BlockChain, forkHeads ...*types.Header)

		// canonicalForkIndex takes variadic fork heads and tells us which
		// should get canonical preference (by index).
		canonicalForkIndex func(forkHeads ...*types.Header) int
	}{
		{
			// SCENARIO-1
			// signers: A..H (8 count)
			//
			/*
				Step 1
				A fully in-order chain exists and validator 8 has just produced an in-turn block.
				1A x
				2B  x
				3C   x
				4D    x
				5E     x
				6F      x
				7G       x
				8H        x
				Step 2
				... and then validators 5, 7, and 8 go offline.
				1A x
				2B  x
				3C   x
				4D    x
				5E     x   -
				6F      x
				7G       x -
				8H        x-
				Step 3
				Two forks form, one with an in-order block from validator 1
				and then an out of order block from validator 3.
				The second fork forms from validators 2, 4, and 6 in order.
				Both have a net total difficulty of 3 more than the common ancestor.
				1A x        y
				2B  x       z
				3C   x       y
				4D    x      z
				5E     x   -
				6F      x     z
				7G       x -
				8H        x-
			*/
			lenSigners:   8,
			commonBlocks: []int{1, 2, 3, 4, 5, 6, 7, 0, 1, 2, 3, 4, 5, 6, 7},
			forks: [][]int{
				{1, 3, 5}, // 2, 4, 6
				{0, 2},    // 1, 3
			},
			canonicalForkIndex: func(forkHeads ...*types.Header) int {
				// Prefer the shorter fork.
				minHeight := math.MaxBig63.Uint64()
				n := -1
				for i, head := range forkHeads {
					if h := head.Number.Uint64(); h < minHeight {
						n = i
						minHeight = h
					}
				}
				return n
			},
			assertions: []func(chain *core.BlockChain, forkHeads ...*types.Header){
				// Assert that the net total difficulties of each fork are equal.
				func(chain *core.BlockChain, forkHeads ...*types.Header) {
					d := new(big.Int)
					for i, head := range forkHeads {
						td := chain.GetTd(head.Hash(), head.Number.Uint64())
						if i == 0 {
							d.Set(td)
							continue
						}
						if d.Cmp(td) != 0 {
							t.Fatalf("want equal fork heads total difficulty")
						}
					}
				},
			},
		},
		{
			// SCENARIO-2

			/*
					Step 1
					For the second scenario with the same validator set and in-order chain with
					validator 7 having just produced an in order block, then validators 7 and 8 go offline.
					1A x
					2B  x
					3C   x
					4D    x
					5E     x
					6F      x
					7G       x
					8H
					1A x
					2B  x
					3C   x
					4D    x
					5E     x
					6F      x
					7G       x-
					8H        -
					Two forks form, 1,3,5 on one side and 2,4,6 on the other.
					Both forks become aware of the other fork after producing their third block.
					In this case both forks have equal total difficulty and equal length.
					1A x       x
					2B  x      y
					3C   x      x
					4D    x     y
					5E     x     x
					6F      x    y
					7G       x-
					8H        -
				FIXME(meowsbits): This scenario yields a "recently signed" error
				when attempting to import Signer 5 (really #6 b/c zero-indexing) into the
				second fork.
				On that fork, the sequence of signers is specified to be
				... 7, 0, 1, 2, 3, 4, 5, 6, 1, 3, 5
				(vs. the other fork)
				... 7, 0, 1, 2, 3, 4, 5, 6, 0, 2, 4
				The condition for "recently signed" is (from *Clique#verifySeal):
				// Signer is among recents, only fail if the current block doesn't shift it out
				if limit := uint64(len(snap.Signers)/2 + 1); seen > number-limit {
					return errRecentlySigned
				}
				Evaluated, this yields
				=> recently signed: limit=(8/2+1)=5 seen=13 number=17 number-limit=12
			*/
			lenSigners:   8,
			commonBlocks: []int{1, 2, 3, 4, 5, 6, 7, 0, 1, 2, 3, 4, 5, 6},
			forks: [][]int{
				{0, 2, 4}, // 1, 3, 5
				{1, 3, 5}, //  2, 4, 6
			},
			canonicalForkIndex: func(forkHeads ...*types.Header) int {
				// Prefer the lowest hash.
				minHashV, _ := uint256.FromBig(big.NewInt(0))
				n := -1
				for i, head := range forkHeads {
					if hv, _ := uint256.FromHex(head.Hash().Hex()); n == -1 || hv.Cmp(minHashV) < 0 {
						minHashV.Set(hv)
						n = i
					}
				}
				return n
			},
			assertions: []func(chain *core.BlockChain, forkHeads ...*types.Header){
				// Assert that the net total difficulties of each fork are equal.
				func(chain *core.BlockChain, forkHeads ...*types.Header) {
					d := new(big.Int)
					for i, head := range forkHeads {
						td := chain.GetTd(head.Hash(), head.Number.Uint64())
						if i == 0 {
							d.Set(td)
							continue
						}
						if d.Cmp(td) != 0 {
							t.Fatalf("want equal fork heads total difficulty")
						}
					}
				},
				// Assert that the block numbers of each fork head are equal.
				func(chain *core.BlockChain, forkHeads ...*types.Header) {
					n := new(big.Int)
					for i, head := range forkHeads {
						if i == 0 {
							n.Set(head.Number)
							continue
						}
						if n.Cmp(head.Number) != 0 {
							t.Fatalf("want equal fork head numbers")
						}
					}
				},
			},
		},
	}

	for ii, tt := range scenarios {
		// Create the account pool and generate the initial set of signerAddressesSorted
		accountsPool := newTesterAccountPool()

		db := rawdb.NewMemoryDatabase()

		// Assemble a chain of headers from the cast votes
		config := *params.TestChainConfig

		cliquePeriod := uint64(1)
		config.Ethash = nil
		config.Clique = &params.CliqueConfig{
			Period:            cliquePeriod,
			Epoch:             0,
			// EIP3436Transition: big.NewInt(0), // TODO: pull this out at add negative test cases (showing that w/o EIP-3436 expectations fail).
		}
		engine := New(config.Clique, db)
		engine.fakeDiff = false

		signerAddressesSorted := make([]common.Address, tt.lenSigners)
		for i := 0; i < tt.lenSigners; i++ {
			signerAddressesSorted[i] = accountsPool.address(fmt.Sprintf("%s", []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K"}[i]))
		}
		for j := 0; j < len(signerAddressesSorted); j++ {
			for k := j + 1; k < len(signerAddressesSorted); k++ {
				if bytes.Compare(signerAddressesSorted[j][:], signerAddressesSorted[k][:]) > 0 {
					signerAddressesSorted[j], signerAddressesSorted[k] = signerAddressesSorted[k], signerAddressesSorted[j]
				}
			}
		}

		// Pretty logging of the sorted signers list.
		logSortedSigners := ""
		for j, s := range signerAddressesSorted {
			logSortedSigners += fmt.Sprintf("%d: (%s) %s\n", j, accountsPool.name(s), s.Hex())
		}
		t.Logf("SORTED SIGNERS:\n%s\n", logSortedSigners)


		// Create the genesis block with the initial set of signerAddressesSorted
		genesis := &core.Genesis{
			ExtraData: make([]byte, extraVanity+common.AddressLength*len(signerAddressesSorted)+extraSeal),
		}
		for j, signer := range signerAddressesSorted {
			copy(genesis.ExtraData[extraVanity+j*common.AddressLength:], signer[:])
		}

		// genesisBlock := core.MustCommitGenesis(db, genesis)
		genesisBlock := genesis.MustCommit(db)

		// Create a pristine blockchain with the genesis injected
		chain, err := core.NewBlockChain(db, nil, &config, engine, vm.Config{}, nil)
		if err != nil {
			t.Errorf("test %d: failed to create test chain: %v", ii, err)
			continue
		}

		// getNextBlockWithSigner generates a block given a parent block and the
		// signer index of the validator that should sign it.
		// It will use the Clique snapshot function to see if the signer is in turn or not,
		// and will assign difficulty appropriately.
		// After signing, it sanity-checks the Engine.Author method against the newly-signed
		// block value to make sure that signing is done properly.
		getNextBlockWithSigner := func(parentBlock *types.Block, signerIndex int) *types.Block {
			signerAddress := signerAddressesSorted[signerIndex]
			signerName := accountsPool.name(signerAddress)

			generatedBlocks, _ := core.GenerateChain(&config, parentBlock, engine, db, 1, nil)
			block := generatedBlocks[0]

			// Get the header and prepare it for signing
			header := block.Header()
			header.ParentHash = parentBlock.Hash()

			header.Time = parentBlock.Time() + cliquePeriod

			// See if our required signer is in or out of turn and assign difficulty respectively.
			difficulty := diffInTurn

			// If the snapshot reports this signer is out of turn, use out-of-turn difficulty.
			snap, err := engine.snapshot(chain, parentBlock.NumberU64(), parentBlock.Hash(), nil)
			if err != nil {
				t.Fatalf("snap err: %v", err)
			}
			inturn := snap.inturn(parentBlock.NumberU64()+1, signerAddress)
			if !inturn {
				difficulty = diffNoTurn
			}
			header.Difficulty = difficulty

			// Generate the signature, embed it into the header and the block.
			header.Extra = make([]byte, extraVanity+extraSeal) // allocate byte slice

			t.Logf("SIGNING: %d (%s) %v %s\n", signerIndex, signerName, inturn, signerAddress.Hex())

			// Sign the header with the associated validator's key.
			accountsPool.sign(header, signerName)

			// Double check to see what the Clique Engine thinks the signer of this block is.
			// It obviously should be the address we just used.
			author, err := engine.Author(header)
			if err != nil {
				t.Fatalf("author error: %v", err)
			}
			if wantSigner := accountsPool.address(signerName); author != wantSigner {
				t.Fatalf("header author != wanted signer: author: %s, signer: %s", author.Hex(), wantSigner.Hex())
			}

			return block.WithSeal(header)
		}

		// Build the common segment
		commonSegmentBlocks := []*types.Block{genesisBlock}
		for i := 0; i < len(tt.commonBlocks); i++ {

			signerIndex := tt.commonBlocks[i]
			parentBlock := commonSegmentBlocks[len(commonSegmentBlocks)-1]

			bl := getNextBlockWithSigner(parentBlock, signerIndex)

			if k, err := chain.InsertChain([]*types.Block{bl}); err != nil || k != 1 {
				t.Fatalf("case: %d, failed to import block %d, count: %d, err: %v", ii, i, k, err)
			}

			commonSegmentBlocks = append(commonSegmentBlocks, bl) // == generatedBlocks[0]
		}

		t.Logf("--- COMMON SEGMENT, td=%v", chain.GetTd(chain.CurrentHeader().Hash(), chain.CurrentHeader().Number.Uint64()))

		forkHeads := make([]*types.Header, len(tt.forks))
		forkTDs := make([]*big.Int, len(tt.forks))

		// Create and import blocks for all the scenario's forks.
		for scenarioForkIndex, forkBlockSigners := range tt.forks {

			forkBlocks := make([]*types.Block, len(commonSegmentBlocks))
			for i, b := range commonSegmentBlocks {
				bcopy := &types.Block{}
				*bcopy = *b
				forkBlocks[i] = bcopy
			}

			for si, signerInt := range forkBlockSigners {

				parent := forkBlocks[len(forkBlocks)-1]
				bl := getNextBlockWithSigner(parent, signerInt)

				if k, err := chain.InsertChain([]*types.Block{bl}); err != nil || k != 1 {
					t.Fatalf("case: %d, failed to import block %d, count: %d, err: %v", ii, si, k, err)
				} else {
					t.Logf("INSERTED block.n=%d TD=%v", bl.NumberU64(), chain.GetTd(bl.Hash(), bl.NumberU64()))
				}
				forkBlocks = append(forkBlocks, bl)

			} // End fork block imports.

			forkHeads[scenarioForkIndex] = chain.CurrentHeader()
			forkTDs[scenarioForkIndex] = chain.GetTd(chain.CurrentHeader().Hash(), chain.CurrentHeader().Number.Uint64())

		} // End scenario fork imports.

		// Run arbitrary assertion tests, ie. make sure that we've created forks that meet
		// the expected scenario characteristics.
		for _, f := range tt.assertions {
			f(chain, forkHeads...)
		}

		// Finally, check that the current chain head matches the
		// head of the wanted fork index.
		if chain.CurrentHeader().Hash() != forkHeads[tt.canonicalForkIndex(forkHeads...)].Hash() {
			forkHeadHashes := ""
			for i, fh := range forkHeads {
				forkHeadHashes += fmt.Sprintf("%d: %s td=%v\n", i, fh.Hash().Hex(), forkTDs[i])
			}
			t.Fatalf(`wrong fork index head: got: %s\nFork heads:\n%s`, chain.CurrentHeader().Hash().Hex(), forkHeadHashes)
		}
	}
}
