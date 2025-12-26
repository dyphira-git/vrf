#!/usr/bin/env sh
set -eo pipefail

go tool buf dep update
go tool buf generate --template ./proto/buf.gen.openapi.yaml

cd gen

yq eval -i 'del(.tags)' openapi.yaml
yq eval -i 'del(.paths[][].tags)' openapi.yaml

yq eval '.paths | keys | .[]' openapi.yaml | while IFS= read -r path; do
  normalizedPath="$path"
  paramName=""
  paramDescription=""

  if [ "$normalizedPath" != "$path" ]; then
    oldPath="$path"
    YQ_OLD_PATH="$oldPath" YQ_NEW_PATH="$normalizedPath" yq eval -i '
      .paths[strenv(YQ_NEW_PATH)] = .paths[strenv(YQ_OLD_PATH)]
    ' openapi.yaml
    # Avoid deleting here to prevent aliasing issues; purge wildcards after loop.
    path="$normalizedPath"
  fi

  if printf '%s' "$path" | grep -q '^/ibc/'; then
    module="ibc"
  else
    module=$(printf '%s' "$path" | cut -d/ -f3)
  fi

  case "$path" in
  # ------ Cosmos: Auth ------
  "/cosmos/auth/v1beta1/accounts/{address}")
    opName="account"
    ;;
  "/cosmos/auth/v1beta1/account_info/{address}")
    opName="account_info"
    ;;
  "/cosmos/auth/v1beta1/address_by_id/{id}")
    opName="address"
    ;;
  "/cosmos/auth/v1beta1/module_accounts/{name}")
    opName="module_account"
    ;;
  "/cosmos/auth/v1beta1/bech32")
    opName="bech32_prefix"
    ;;
  "/cosmos/auth/v1beta1/bech32/{addressBytes}")
    opName="address_from_bytes"
    ;;
  "/cosmos/auth/v1beta1/bech32/{addressString}")
    opName="address_from_string"
    ;;
  # ------ Cosmos: Bank ------
  "/cosmos/bank/v1beta1/spendable_balances/{address}/by_denom")
    opName="spendable_balances_by_denom"
    ;;
  "/cosmos/bank/v1beta1/denoms_metadata/{denom}")
    opName="denom_metadata"
    ;;
  "/cosmos/bank/v1beta1/supply/by_denom")
    opName="supply_by_denom"
    ;;
  # ----- Cosmos: Base ------
  "/cosmos/base/tendermint/v1beta1/validatorsets/{height}")
    opName="validators_at_height"
    ;;
  "/cosmos/base/tendermint/v1beta1/abci_query")
    opName="ABCI_query"
    ;;
  "/cosmos/base/tendermint/v1beta1/blocks/{height}")
    opName="block"
    ;;
  "/cosmos/base/tendermint/v1beta1/validatorsets/latest")
    opName="current_validators"
    ;;
  /cosmos/base/tendermint/v1beta1/blocks/latest)
    opName="latest_block"
    ;;
  # ------ Cosmos: Distribution ------
  "/cosmos/distribution/v1beta1/validators/{validatorAddress}")
    opName="validator_rewards"
    ;;
  "/cosmos/distribution/v1beta1/delegators/{delegatorAddress}/rewards/{validatorAddress}")
    opName="rewards_by_validator"
    ;;
  # ------ Cosmos: Feegrant ------
  "/cosmos/feegrant/v1beta1/allowance/{granter}/{grantee}")
    opName="allowance"
    ;;
  "/cosmos/feegrant/v1beta1/issued/{granter}")
    opName="issued"
    ;;
  # ------ Cosmos: Gov ------
  "/cosmos/gov/v1/proposals/{proposalId}")
    opName="proposal"
    ;;
  "/cosmos/gov/v1/proposals/{proposalId}/deposits/{depositor}")
    opName="proposal_deposit_by_depositor"
    ;;
  "/cosmos/gov/v1/proposals/{proposalId}/votes/{voter}")
    opName="vote_by_voter"
    ;;
  "/cosmos/gov/v1beta1/params/{paramsType}")
    opName="v1beta1_params"
    ;;
  "/cosmos/gov/v1beta1/proposals/{proposalId}/deposits/{depositor}")
    opName="v1beta1_proposal_deposit_by_depositor"
    ;;
  "/cosmos/gov/v1beta1/proposals/{proposalId}/deposits")
    opName="v1beta1_proposal_deposits"
    ;;
  "/cosmos/gov/v1beta1/proposals/{proposalId}")
    opName="v1beta1_proposal"
    ;;
  "/cosmos/gov/v1beta1/proposals")
    opName="v1beta1_proposals"
    ;;
  "/cosmos/gov/v1beta1/proposals/{proposalId}/tally")
    opName="v1beta1_proposal_tally"
    ;;
  "/cosmos/gov/v1beta1/proposals/{proposalId}/votes/{voter}")
    opName="v1beta1_vote_by_voter"
    ;;
  "/cosmos/gov/v1beta1/proposals/{proposalId}/votes")
    opName="v1beta1_votes"
    ;;
  # ------ Cosmos: Group------
  "/cosmos/group/v1/vote_by_proposal_voter/{proposalId}/{voter}")
    opName="voter_votes_by_proposal"
    ;;
  # ------ Cosmos: NFT ------
  "/cosmos/nft/v1beta1/classes/{classId}")
    opName="class"
    ;;
  "/cosmos/nft/v1beta1/owner/{classId}/{id}")
    opName="owner"
    ;;
  "/cosmos/nft/v1beta1/supply/{classId}")
    opName="supply"
    ;;
  "/cosmos/nft/v1beta1/balance/{owner}/{classId}")
    opName="balance"
    ;;
  "/cosmos/nft/v1beta1/nfts/{classId}/{id}")
    opName="NFT_from_collection"
    ;;
  "/cosmos/nft/v1beta1/nfts")
    opName="NFTs"
    ;;
  # ------ Cosmos: Staking ------
  "/cosmos/staking/v1beta1/validators/{validatorAddr}/delegations/{delegatorAddr}")
    opName="validator_delegation_by_delegator"
    ;;
  "/cosmos/staking/v1beta1/validators/{validatorAddr}/unbonding_delegations")
    opName="validator_unbonding_delegations"
    ;;
  "/cosmos/staking/v1beta1/validators/{validatorAddr}")
    opName="validator"
    ;;
  "/cosmos/staking/v1beta1/delegators/{delegatorAddr}/validators/{validatorAddr}")
    opName="validator_by_delegator"
    ;;
  "/cosmos/staking/v1beta1/validators")
    opName="validators"
    ;;
  # ------ Cosmos: Slashing ------
  "/cosmos/slashing/v1beta1/signing_infos/{consAddress}")
    opName="signing_info"
    ;;
  # ------ Cosmos: Evidence ------
  "/cosmos/evidence/v1beta1/evidence")
    opName="evidences"
    ;;
  "/cosmos/evidence/v1beta1/evidence/{hash}")
    opName="evidence"
    ;;
  # ------ Cosmos: Tx ------
  "/cosmos/tx/v1beta1/txs/block/{height}")
    opName="get_txs_by_block_height"
    ;;
  "/cosmos/tx/v1beta1/txs/{hash}")
    opName="get_tx_by_hash"
    ;;
  *)
    opName="${path##*/}"
    ;;
  esac

  for method in get post; do
    if yq eval ".paths[\"$path\"].$method" openapi.yaml | grep -qv '^null$'; then
      # sanitize opName if it contains path params like {denom}
      if printf '%s' "$opName" | grep -q '{'; then
        baseSegment=$(printf '%s' "$path" | awk -F/ '{print $(NF-1)}')
        opName="$baseSegment"
      fi

      newOpId="${module}_${opName}"

      if [ -n "$paramName" ]; then
        YQ_PATH="$path" YQ_METHOD="$method" YQ_PARAM="$paramName" YQ_DESC="$paramDescription" \
          yq eval -i '
            .paths[strenv(YQ_PATH)][env(YQ_METHOD)].parameters = (
              (.paths[strenv(YQ_PATH)][env(YQ_METHOD)].parameters // [])
              | map(select(.name != env(YQ_PARAM) or .in != "path"))
              + [{
                "name": env(YQ_PARAM),
                "in": "path",
                "required": true,
                "schema": {"type": "string"},
                "description": env(YQ_DESC)
              }]
            )
          ' openapi.yaml
      fi

      # set operationId
      yq eval -i \
        '.paths["'"$path"'"].'"$method"'.operationId = "'"$newOpId"'"' \
        openapi.yaml

      # set a human-friendly summary for display (Title Case of opName)
      prettyName=$(printf '%s' "$opName" | tr '_' ' ' | awk '{for(i=1;i<=NF;i++){ $i=toupper(substr($i,1,1)) substr($i,2)}; print}')
      YQ_PATH="$path" YQ_METHOD="$method" YQ_SUMMARY="$prettyName" yq eval -i \
        '.paths[strenv(YQ_PATH)][env(YQ_METHOD)].summary = strenv(YQ_SUMMARY)' \
        openapi.yaml

      # set the tag array; force acronyms/names to desired forms
      tagName="$module"
      if [ "$module" = "ibc" ]; then
        tagName="IBC"
      elif [ "$module" = "nft" ]; then
        tagName="NFT"
      elif [ "$module" = "cwhooks" ]; then
        tagName="Hooks"
      elif [ "$module" = "wasm" ]; then
        tagName="CosmWasm"
      fi
      yq eval -i \
        '.paths["'"$path"'"].'"$method"'.tags = ["'"$tagName"'"]' \
        openapi.yaml
    fi
  done
done

# collect all tags from the paths, remove duplicates and add them to the tags array
yq eval -i '
  .tags = (
    [ .paths.*.*.tags[] ]
    | unique
    | map({"name": ., "description": ""})
  )
' openapi.yaml

# remove any leftover wildcard (**) paths after normalization to avoid duplicates
yq eval -i '
  .paths |= with_entries(select(.key | contains("**") | not))
' openapi.yaml

# remove duplicate comment from merging files
tail -n +4 openapi.yaml >tmp && mv tmp openapi.yaml

# capitalize all tags
yq eval -i '
  .tags[].name |= (
    capture("(?<first>.)(?<rest>.*)")
    | .first |= upcase
    | .first + .rest
  ) |

  .paths[][].tags |= map(
    capture("(?<first>.)(?<rest>.*)")
    | .first |= upcase
    | .first + .rest
  )
' openapi.yaml

# sort all path keys alphabetically
yq eval -i '.paths = (.paths | to_entries | sort_by(.key) | from_entries)' openapi.yaml

# sort top-level tags alphabetically
yq eval -i '.tags |= sort_by(.name)' openapi.yaml

# sort components alphabetically
yq eval -i '
  .components.schemas = ((.components.schemas // {}) | to_entries | sort_by(.key) | from_entries)
' openapi.yaml

# move the final openapi.yaml to the correct location
mv openapi.yaml ../app/endpoints/openapi.yaml

cd ..
rm -rf gen
