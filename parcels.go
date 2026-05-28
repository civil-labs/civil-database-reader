package main

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	parcelsv1 "github.com/civil-labs/civil-api-go/civil/mesh/parcels/v1"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ParcelServer struct {
	db     *pgxpool.Pool
	logger *slog.Logger
}

func (s *ParcelServer) GetParcelsById(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetParcelsByIdRequest],
) (*connect.Response[parcelsv1.GetParcelsByIdResponse], error) {

	s.logger.Debug("received GetParcelsById request", slog.Any("parcelIds", req.Msg.ParcelIds))

	parcelIds := req.Msg.ParcelIds

	var neighborhoodDefIDArg *string

	// If the string is NOT empty, we take its memory address (&)
	// to create a pointer to the string.
	if defID := req.Msg.GetNeighborhoodDefinitionId(); defID != "" {
		neighborhoodDefIDArg = &defID
	}

	// Empty arrays are blocked by the connect handler via the proto def

	s.logger.Debug("creating GetParcelsById map")

	parcels := make(map[string]*parcelsv1.Parcel, len(parcelIds))

	s.logger.Debug("building GetParcelsById query")

	// Use defensive truncation to match what is returned from unbounded text columns
	// to the max value promised in the proto contract
	query := `
        SELECT 
            p.public_id::text,
            LEFT(aa.formatted_address, 1024) AS safe_address,
            a.public_id::text,
            LEFT(parties_agg.primary_owner_name, 128) AS safe_primary_owner_name,
            LEFT(parties_agg.primary_owner_address, 256) AS safe_primary_owner_address,
            parties_agg.party_ids,
            pa.land_area_sq_m,
            pa.frontage_m,
            pa.depth_m,
            lu.public_id::text,
            n.public_id::text,

            aff.zoning_public_ids,
            aff.affordance_ids,
            aff.strict_max_far,
            aff.strict_min_lot_size_sq_m,
            aff.strict_max_height_m,
			aff.strict_max_dwelling_units_per_hectare,
			aff.strict_max_lot_coverage_pct,

            pa.properties::text
        FROM parcels p
        LEFT JOIN parcel_attributes pa ON p.parcel_id = pa.parcel_id 
        LEFT JOIN addresses a ON pa.address_id = a.address_id
        LEFT JOIN address_attributes aa ON a.address_id = aa.address_id
        LEFT JOIN land_uses lu ON pa.land_use_id = lu.land_use_id

        -- New Party / Fractional Ownership Aggregation
        LEFT JOIN (
            SELECT 
                pp.parcel_id,
                -- Aggregate all unique party IDs, ordered by who holds the largest share
                array_agg(pty.public_id::text ORDER BY pp.ownership_share DESC, pty.party_id ASC) AS party_ids,
                -- Grab the name and address of the party with the highest ownership share to act as the "Primary"
                (array_agg(pa_attr.name ORDER BY pp.ownership_share DESC, pty.party_id ASC))[1] AS primary_owner_name,
                (array_agg(p_aa.formatted_address ORDER BY pp.ownership_share DESC, pty.party_id ASC))[1] AS primary_owner_address
            FROM parcel_parties pp
            JOIN parties pty ON pp.party_id = pty.party_id
            LEFT JOIN party_attributes pa_attr ON pty.party_id = pa_attr.party_id
            LEFT JOIN address_attributes p_aa ON pa_attr.address_id = p_aa.address_id
            GROUP BY pp.parcel_id
        ) parties_agg ON p.parcel_id = parties_agg.parcel_id

        -- Neighborhood Definition Joins
        LEFT JOIN neighborhood_definitions nd 
            ON nd.public_id = $2::uuid
        LEFT JOIN parcel_neighborhood_definitions pnd 
            ON p.parcel_id = pnd.parcel_id 
            AND pnd.neighborhood_definition_id = nd.neighborhood_definition_id
        LEFT JOIN neighborhoods n 
            ON pnd.neighborhood_id = n.neighborhood_id

        -- Aggregated Affordances Subquery
        LEFT JOIN (
            SELECT 
                parcel_id,
                array_remove(array_agg(DISTINCT z.public_id::text), NULL) AS zoning_public_ids,
                array_remove(array_agg(DISTINCT pa.public_id::text), NULL) AS affordance_ids,
                MIN(max_far) AS strict_max_far,
                MAX(min_lot_size_sq_m) AS strict_min_lot_size_sq_m,
                MIN(max_height_m) AS strict_max_height_m,
				MIN(max_dwelling_units_per_hectare) AS strict_max_dwelling_units_per_hectare,
				MIN(max_lot_coverage_pct) AS strict_max_lot_coverage_pct
            FROM parcel_affordances pa
			LEFT JOIN zoning z ON pa.zoning_id = z.zoning_id
            GROUP BY parcel_id
        ) aff ON p.parcel_id = aff.parcel_id

        WHERE p.public_id = ANY($1::uuid[])
    `

	rows, err := s.db.Query(ctx, query, parcelIds, neighborhoodDefIDArg)

	s.logger.Debug("performed GetParcelsById query")

	if err != nil {
		s.logger.Error("failed to query parcels", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to retrieve parcel data"))
	}
	defer rows.Close()

	for rows.Next() {
		s.logger.Debug("scanning row")

		// 1. Declare pointers for optional fields
		var (
			parcelID            string
			address             *string
			addressID           *string
			primaryOwnerName    *string
			primaryOwnerAddress *string
			partyIDs            []string
			landAreaSqM         *float64
			frontageM           *float64
			depthM              *float64
			landUseID           *string
			neighborhoodID      *string

			zoningIDs               []string
			affordanceIDs           []string
			maxFar                  *float64
			minLotSizeSqM           *float64
			maxHeightM              *float64
			maxDwellingUnitsPerHect *float64
			maxLotCoveragePct       *float64

			properties *string
		)

		// 2. Scan directly into the pointers
		err := rows.Scan(
			&parcelID,
			&address,
			&addressID,
			&primaryOwnerName,
			&primaryOwnerAddress,
			&partyIDs,
			&landAreaSqM,
			&frontageM,
			&depthM,
			&landUseID,
			&neighborhoodID,

			&zoningIDs,
			&affordanceIDs,
			&maxFar,
			&minLotSizeSqM,
			&maxHeightM,
			&maxDwellingUnitsPerHect,
			&maxLotCoveragePct,

			&properties,
		)

		s.logger.Debug("scanned row", slog.Any("parcelId", parcelID))

		if err != nil {
			s.logger.Error("failed to scan parcel row", "error", err, "parcelId", parcelID)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("data unmarshaling error"))
		}

		// 3. Metric to Imperial Conversions (safely handling nils)
		var landAreaSqFt, frontageFt, depthFt, minLotSizeSqFt, maxHeightFt *float64

		if landAreaSqM != nil {
			// 1 sq meter = 10.7639 sq feet
			val := *landAreaSqM * 10.7639
			landAreaSqFt = &val
		}
		if frontageM != nil {
			// 1 meter = 3.28084 feet
			val := *frontageM * 3.28084
			frontageFt = &val
		}
		if depthM != nil {
			val := *depthM * 3.28084
			depthFt = &val
		}

		if minLotSizeSqM != nil {
			val := *minLotSizeSqM * 10.7639
			minLotSizeSqFt = &val
		}
		if maxHeightM != nil {
			val := *maxHeightM * 3.28084
			maxHeightFt = &val
		}

		s.logger.Debug("converted units")

		// If nothing was found, initialize empty slices
		if zoningIDs == nil {
			zoningIDs = []string{}
		}
		if affordanceIDs == nil {
			affordanceIDs = []string{}
		}

		affordances := &parcelsv1.ParcelAffordances{
			AffordanceIds:  affordanceIDs,
			MaxFar:         maxFar,
			MinLotSizeSqFt: minLotSizeSqFt,
			MaxHeightFt:    maxHeightFt,
		}

		improvementSummary := &parcelsv1.ParcelImprovementsSummary{
			ImprovementIds: []string{}, // Guarantees a non-nil slice
		}

		// 4. Populate the Protobuf map
		// Assuming your proto generates pointers (*string, *float64) for optional fields
		parcels[parcelID] = &parcelsv1.Parcel{
			ParcelId:            parcelID,
			Address:             address,
			AddressId:           addressID,
			PrimaryOwnerName:    primaryOwnerName,
			PrimaryOwnerAddress: primaryOwnerAddress,
			PartyIds:            partyIDs,
			LandUseId:           landUseID,
			NeighborhoodId:      neighborhoodID,
			LandAreaSqFt:        landAreaSqFt,
			FrontageFt:          frontageFt,
			DepthFt:             depthFt,
			ZoningIds:           zoningIDs,
			Affordances:         affordances,
			ImprovementSummary:  improvementSummary,
			Properties:          properties,
		}

		s.logger.Debug("populated protobuf map")
	}

	if err := rows.Err(); err != nil {
		s.logger.Error("error iterating parcel rows", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to process parcel stream"))
	}

	s.logger.Debug("constructing response")

	// Wrap the map in your response payload
	res := &parcelsv1.GetParcelsByIdResponse{
		Parcels: parcels,
	}

	s.logger.Debug("sending response")

	return connect.NewResponse(res), nil
}

func (s *ParcelServer) UpdateParcel(
	ctx context.Context,
	req *connect.Request[parcelsv1.UpdateParcelRequest],
) (*connect.Response[parcelsv1.UpdateParcelResponse], error) {

	// if req.Msg.ParcelId == "" {
	// 	return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("parcel ID is required"))
	// }
	// if req.Msg.PropertyName == "" {
	// 	return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("property name is required"))
	// }

	// query := `SELECT properties ->> $2 FROM parcels WHERE id = $1`

	// // Execute Query
	// var value *string // Use a pointer to handle nulls if the key doesn't exist in the JSON
	// err := s.db.QueryRow(ctx, query, req.Msg.ParcelId, req.Msg.PropertyName).Scan(&value)

	// if err != nil {
	// 	if errors.Is(err, pgx.ErrNoRows) {
	// 		return nil, connect.NewError(connect.CodeNotFound, errors.New("parcel not found"))
	// 	}
	// 	return nil, connect.NewError(connect.CodeInternal, errors.New("database error"))
	// }

	// if value == nil {
	// 	msg := fmt.Sprintf("property %s not found for parcel %s", req.Msg.PropertyName, req.Msg.ParcelId)
	// 	return nil, connect.NewError(connect.CodeNotFound, errors.New(msg))
	// }

	res := &parcelsv1.UpdateParcelResponse{}
	return connect.NewResponse(res), nil
}

func (s *ParcelServer) GetCategoricalParcelStatsById(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetCategoricalParcelStatsByIdRequest],
) (*connect.Response[parcelsv1.GetCategoricalParcelStatsByIdResponse], error) {

	// if req.Msg.ParcelId == "" {
	// 	return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("parcel ID is required"))
	// }

	// query := `SELECT * FROM parcels WHERE id = $1`

	// // Execute Query
	// var value *string // Use a pointer to handle nulls if the key doesn't exist in the JSON
	// err := s.db.QueryRow(ctx, query, req.Msg.ParcelId).Scan(&value)

	// if err != nil {
	// 	if errors.Is(err, pgx.ErrNoRows) {
	// 		return nil, connect.NewError(connect.CodeNotFound, errors.New("parcel not found"))
	// 	}
	// 	return nil, connect.NewError(connect.CodeInternal, errors.New("database error"))
	// }

	// if value == nil {
	// 	msg := fmt.Sprintf("parcel %s not found", req.Msg.ParcelId)
	// 	return nil, connect.NewError(connect.CodeNotFound, errors.New(msg))
	// }

	res := &parcelsv1.GetCategoricalParcelStatsByIdResponse{}

	return connect.NewResponse(res), nil
}

func (s *ParcelServer) GetNumericalParcelStatsById(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetNumericalParcelStatsByIdRequest],
) (*connect.Response[parcelsv1.GetNumericalParcelStatsByIdResponse], error) {

	// if req.Msg.ParcelId == "" {
	// 	return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("parcel ID is required"))
	// }

	// query := `SELECT * FROM parcels WHERE id = $1`

	// // Execute Query
	// var value *string // Use a pointer to handle nulls if the key doesn't exist in the JSON
	// err := s.db.QueryRow(ctx, query, req.Msg.ParcelId).Scan(&value)

	// if err != nil {
	// 	if errors.Is(err, pgx.ErrNoRows) {
	// 		return nil, connect.NewError(connect.CodeNotFound, errors.New("parcel not found"))
	// 	}
	// 	return nil, connect.NewError(connect.CodeInternal, errors.New("database error"))
	// }

	// if value == nil {
	// 	msg := fmt.Sprintf("parcel %s not found", req.Msg.ParcelId)
	// 	return nil, connect.NewError(connect.CodeNotFound, errors.New(msg))
	// }

	res := &parcelsv1.GetNumericalParcelStatsByIdResponse{}

	return connect.NewResponse(res), nil
}
