package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	parcelsv1 "github.com/civil-labs/civil-api-go/civil/mesh/parcels/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *APIServer) GetParcelsById(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetParcelsByIdRequest],
) (*connect.Response[parcelsv1.GetParcelsByIdResponse], error) {

	s.logger.Debug("received GetParcelsById request", slog.Any("parcelIds", req.Msg.ParcelIds),
		slog.Any("neighborhoodDefinitionId", req.Msg.NeighborhoodDefinitionId),
		slog.Any("valuationId", req.Msg.ValuationId))

	parcelIds := req.Msg.ParcelIds

	var neighborhoodDefIDArg *string

	// If the string is NOT empty, we take its memory address (&)
	// to create a pointer to the string.
	if defID := req.Msg.GetNeighborhoodDefinitionId(); defID != "" {
		neighborhoodDefIDArg = &defID
	}

	var valuationIDArg *string
	if valID := req.Msg.GetValuationId(); valID != "" {
		valuationIDArg = &valID
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

			pv_agg.market_land_value,
			pv_agg.assessed_land_value,

			imp_agg.improvement_ids,
			COALESCE(imp_agg.total_area_sq_m, 0),
			COALESCE(imp_agg.total_bathrooms, 0),
			COALESCE(imp_agg.total_bedrooms, 0),
			COALESCE(imp_agg.total_units, 0),
			imp_agg.oldest_year_built,
			imp_agg.newest_year_built,
			imp_agg.weighted_average_depreciation_modifier,
			imp_agg.worst_condition_id,
			imp_agg.best_condition_id,
			imp_agg.market_improvement_value,
			imp_agg.assessed_improvement_value,

            pa.properties::text,
            COALESCE(pg.feature_id, 0)
        FROM parcels p
        LEFT JOIN parcel_geometry pg ON p.parcel_id = pg.parcel_id
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

        -- Aggregated Parcel Valuations Subquery
        LEFT JOIN (
            SELECT 
                pv.parcel_id,
                pv.market_value::numeric(19,4)::text AS market_land_value,
                pv.assessed_value::numeric(19,4)::text AS assessed_land_value
            FROM parcel_valuations pv
            JOIN valuations v ON pv.valuation_id = v.valuation_id
            WHERE $3::uuid IS NOT NULL AND v.public_id = $3::uuid
        ) pv_agg ON p.parcel_id = pv_agg.parcel_id

        -- Aggregated Improvements Summary Subquery
        LEFT JOIN (
            SELECT 
                pi.parcel_id,
                array_remove(array_agg(DISTINCT imp.public_id::text), NULL) AS improvement_ids,
                COALESCE(SUM(attr.area_sq_m), 0) AS total_area_sq_m,
                COALESCE(SUM(attr.bathrooms), 0) AS total_bathrooms,
                COALESCE(SUM(attr.bedrooms), 0) AS total_bedrooms,
                COALESCE(SUM(attr.units), 0) AS total_units,
                MIN(attr.year_built) AS oldest_year_built,
                MAX(attr.year_built) AS newest_year_built,
                (SUM(COALESCE(attr.area_sq_m, 1) * cond_attr.depreciation_modifier) / NULLIF(SUM(COALESCE(attr.area_sq_m, 1)), 0))::numeric(5,4)::text AS weighted_average_depreciation_modifier,
                (array_agg(cond.public_id::text ORDER BY cond_attr.depreciation_modifier ASC, cond.improvement_condition_id ASC))[1] AS worst_condition_id,
                (array_agg(cond.public_id::text ORDER BY cond_attr.depreciation_modifier DESC, cond.improvement_condition_id ASC))[1] AS best_condition_id,
                SUM(val.market_value)::numeric(19,4)::text AS market_improvement_value,
                SUM(val.assessed_value)::numeric(19,4)::text AS assessed_improvement_value
            FROM parcel_improvements pi
            JOIN improvements imp ON pi.improvement_id = imp.improvement_id
            LEFT JOIN improvement_attributes attr ON imp.improvement_id = attr.improvement_id
            LEFT JOIN improvement_conditions cond ON attr.improvement_condition_id = cond.improvement_condition_id AND NOT cond.is_voided
            LEFT JOIN improvement_condition_attributes cond_attr ON cond.improvement_condition_id = cond_attr.improvement_condition_id
            LEFT JOIN improvement_valuations val ON imp.improvement_id = val.improvement_id
                AND $3::uuid IS NOT NULL
                AND val.valuation_id = (
                    SELECT v.valuation_id 
                    FROM valuations v 
                    WHERE v.public_id = $3::uuid
                )
            WHERE NOT imp.is_voided
            GROUP BY pi.parcel_id
        ) imp_agg ON p.parcel_id = imp_agg.parcel_id

        WHERE p.public_id = ANY($1::uuid[])
    `

	rows, err := s.db.Query(ctx, query, parcelIds, neighborhoodDefIDArg, valuationIDArg)

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

			marketLandValue   *string
			assessedLandValue *string

			improvementIDs   []string
			totalAreaSqM     float64
			totalBathrooms   int32
			totalBedrooms    int32
			totalUnits       int32
			oldestYearBuilt  *int32
			newestYearBuilt  *int32
			weightedDeprMod  *string
			worstConditionID *string
			bestConditionID  *string
			marketImpValue   *string
			assessedImpValue *string

			properties *string
			featureID  int64
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

			&marketLandValue,
			&assessedLandValue,

			&improvementIDs,
			&totalAreaSqM,
			&totalBathrooms,
			&totalBedrooms,
			&totalUnits,
			&oldestYearBuilt,
			&newestYearBuilt,
			&weightedDeprMod,
			&worstConditionID,
			&bestConditionID,
			&marketImpValue,
			&assessedImpValue,

			&properties,
			&featureID,
		)

		s.logger.Debug("scanned row", slog.Any("parcelId", parcelID))

		if err != nil {
			s.logger.Error("failed to scan parcel row", "error", err, "parcelId", parcelID)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("data unmarshaling error"))
		}

		// 3. Metric to Imperial Conversions (safely handling nils)
		var landAreaSqFt, frontageFt, depthFt, minLotSizeSqFt, maxHeightFt, maxDwellingUnitsPerAcre *float64

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

		if maxDwellingUnitsPerHect != nil {
			val := *maxDwellingUnitsPerHect * 0.404686
			maxDwellingUnitsPerAcre = &val
		}

		totalAreaSqFt := totalAreaSqM * 10.7639

		s.logger.Debug("converted units")

		// If nothing was found, initialize empty slices
		if zoningIDs == nil {
			zoningIDs = []string{}
		}
		if affordanceIDs == nil {
			affordanceIDs = []string{}
		}
		if improvementIDs == nil {
			improvementIDs = []string{}
		}

		affordances := &parcelsv1.ParcelAffordances{
			AffordanceIds:           affordanceIDs,
			MaxFar:                  maxFar,
			MinLotSizeSqFt:          minLotSizeSqFt,
			MaxHeightFt:             maxHeightFt,
			MaxDwellingUnitsPerAcre: maxDwellingUnitsPerAcre,
			MaxLotCoveragePct:       maxLotCoveragePct,
		}

		improvementSummary := &parcelsv1.ParcelImprovementsSummary{
			ImprovementIds:                      improvementIDs,
			TotalAreaSqFt:                       totalAreaSqFt,
			TotalBathrooms:                      totalBathrooms,
			TotalBedrooms:                       totalBedrooms,
			TotalUnits:                          totalUnits,
			OldestYearBuilt:                     oldestYearBuilt,
			NewestYearBuilt:                     newestYearBuilt,
			WeightedAverageDepreciationModifier: weightedDeprMod,
			WorstConditionId:                    worstConditionID,
			BestConditionId:                     bestConditionID,
			MarketImprovementValue:              marketImpValue,
			AssessedImprovementValue:            assessedImpValue,
		}

		// 4. Populate the Protobuf map
		// Assuming your proto generates pointers (*string, *float64) for optional fields
		parcels[parcelID] = &parcelsv1.Parcel{
			ParcelId:            parcelID,
			FeatureId:           featureID,
			FormattedAddress:    address,
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
			MarketLandValue:     marketLandValue,
			AssessedLandValue:   assessedLandValue,
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

func (s *APIServer) UpdateParcel(
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

func (s *APIServer) GetCategoricalParcelStatsById(
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

func (s *APIServer) GetNumericalParcelStatsById(
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

type CompParcelInfo struct {
	ParcelID            string
	AddressID           string
	FormattedAddress    string
	LandAreaSqFt        *float64
	FrontageFt          *float64
	DepthFt             *float64
	LandUseID           *string
	ZoningIDs           []string
	ImprovementAreaSqFt float64
	YearBuilt           *int32
	EffectiveYearBuilt  *int32
	Bedrooms            int32
	Bathrooms           int32
	Units               int32
	ConditionIDs        []string
	ImprovementTypeIDs  []string
	SaleTime            *time.Time
	SalePrice           *string
	FeatureID           int64
}

// fetchParcelsForComps retrieves candidate parcels and their metadata attributes.
// To maximize performance and scale efficiently, it dynamically compiles the SQL query,
// appending SELECT columns, LEFT JOIN clauses, and row scan destinations (scanDest)
// ONLY for the attributes specified in the criteria list or if sales information is requested.
func (s *APIServer) fetchParcelsForComps(
	ctx context.Context,
	candidateIDs []string,
	wktPolygon *string,
	startTime *time.Time,
	endTime *time.Time,
	criteria []*parcelsv1.ComparableCriteria,
	isSales bool,
) (map[string]*CompParcelInfo, error) {

	// Baseline fields that are always queried for every comparable parcel
	var selectFields = []string{
		"p.public_id::text",
		"LEFT(aa.formatted_address, 1024) AS safe_address",
		"a.public_id::text",
		"pa.land_area_sq_m",
		"pa.frontage_m",
		"pa.depth_m",
		"COALESCE(pg.feature_id, 0)",
	}

	// Baseline joins required for the default parcel attributes
	var joins = []string{
		"LEFT JOIN parcel_attributes pa ON p.parcel_id = pa.parcel_id",
		"LEFT JOIN addresses a ON pa.address_id = a.address_id",
		"LEFT JOIN address_attributes aa ON a.address_id = aa.address_id",
		"LEFT JOIN parcel_geometry pg ON p.parcel_id = pg.parcel_id",
	}

	// Variables where scanned column data is stored
	var (
		parcelID                 string
		address                  *string
		addressID                *string
		landAreaSqM              *float64
		frontageM                *float64
		depthM                   *float64
		landUseID                *string
		zoningIDs                []string
		improvementIDs           []string
		improvementTypeIDs       []string
		conditionIDs             []string
		totalAreaSqM             float64
		totalBathrooms           int32
		totalBedrooms            int32
		totalUnits               int32
		oldestYearBuilt          *int32
		newestYearBuilt          *int32
		oldestEffectiveYearBuilt *int32
		newestEffectiveYearBuilt *int32
		saleTime                 *time.Time
		salePrice                *string
		featureID                int64
	)

	// Baseline pointers matching the order of selectFields
	var scanDest = []any{
		&parcelID,
		&address,
		&addressID,
		&landAreaSqM,
		&frontageM,
		&depthM,
		&featureID,
	}

	var joinedImprovements, joinedZoning, joinedLandUse bool

	// Dynamic Query Assembly based on the input criteria
	for _, c := range criteria {
		if c == nil {
			continue
		}
		switch c.GetAttribute() {
		case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_LAND_USE_ID:
			// Add land use mapping and fetch the public ID
			if !joinedLandUse {
				joins = append(joins, "LEFT JOIN land_uses lu ON pa.land_use_id = lu.land_use_id")
				selectFields = append(selectFields, "lu.public_id::text")
				scanDest = append(scanDest, &landUseID)
				joinedLandUse = true
			}

		case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_ZONING_ID:
			// Aggregate all unique zoning IDs for the parcel
			if !joinedZoning {
				joins = append(joins, `
                    LEFT JOIN (
                        SELECT 
                            parcel_id,
                            array_remove(array_agg(DISTINCT z.public_id::text), NULL) AS zoning_public_ids
                        FROM parcel_affordances pa
                        LEFT JOIN zoning z ON pa.zoning_id = z.zoning_id
                        GROUP BY parcel_id
                    ) aff ON p.parcel_id = aff.parcel_id`)
				selectFields = append(selectFields, "aff.zoning_public_ids")
				scanDest = append(scanDest, &zoningIDs)
				joinedZoning = true
			}

		case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_IMPROVEMENT_AREA_SQ_FT,
			parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_IMPROVEMENT_YEAR_BUILT,
			parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_IMPROVEMENT_EFFECTIVE_YEAR_BUILT,
			parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_BEDROOMS,
			parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_BATHROOMS,
			parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_UNITS,
			parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_CONDITION_ID,
			parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_IMPROVEMENT_TYPE_ID:
			// Join improvement attributes subquery to fetch size, rooms, conditions, and years built
			if !joinedImprovements {
				joins = append(joins, `
                    LEFT JOIN (
                        SELECT 
                            pi.parcel_id,
                            array_remove(array_agg(DISTINCT imp.public_id::text), NULL) AS improvement_ids,
                            array_remove(array_agg(DISTINCT imptype.public_id::text), NULL) AS improvement_type_ids,
                            array_remove(array_agg(DISTINCT cond.public_id::text), NULL) AS condition_ids,
                            COALESCE(SUM(attr.area_sq_m), 0) AS total_area_sq_m,
                            COALESCE(SUM(attr.bathrooms), 0) AS total_bathrooms,
                            COALESCE(SUM(attr.bedrooms), 0) AS total_bedrooms,
                            COALESCE(SUM(attr.units), 0) AS total_units,
                            MIN(attr.year_built) AS oldest_year_built,
                            MAX(attr.year_built) AS newest_year_built,
                            MIN(NULLIF(attr.properties->>'effective_year_built', '')::int) AS oldest_effective_year_built,
                            MAX(NULLIF(attr.properties->>'effective_year_built', '')::int) AS newest_effective_year_built
                        FROM parcel_improvements pi
                        JOIN improvements imp ON pi.improvement_id = imp.improvement_id
                        LEFT JOIN improvement_attributes attr ON imp.improvement_id = attr.improvement_id
                        LEFT JOIN improvement_types imptype ON attr.improvement_type_id = imptype.improvement_type_id
                        LEFT JOIN improvement_conditions cond ON attr.improvement_condition_id = cond.improvement_condition_id AND NOT cond.is_voided
                        WHERE NOT imp.is_voided
                        GROUP BY pi.parcel_id
                    ) imp_agg ON p.parcel_id = imp_agg.parcel_id`)
				selectFields = append(selectFields,
					"imp_agg.improvement_ids",
					"imp_agg.improvement_type_ids",
					"imp_agg.condition_ids",
					"COALESCE(imp_agg.total_area_sq_m, 0)",
					"COALESCE(imp_agg.total_bathrooms, 0)",
					"COALESCE(imp_agg.total_bedrooms, 0)",
					"COALESCE(imp_agg.total_units, 0)",
					"imp_agg.oldest_year_built",
					"imp_agg.newest_year_built",
					"imp_agg.oldest_effective_year_built",
					"imp_agg.newest_effective_year_built",
				)
				scanDest = append(scanDest,
					&improvementIDs,
					&improvementTypeIDs,
					&conditionIDs,
					&totalAreaSqM,
					&totalBathrooms,
					&totalBedrooms,
					&totalUnits,
					&oldestYearBuilt,
					&newestYearBuilt,
					&oldestEffectiveYearBuilt,
					&newestEffectiveYearBuilt,
				)
				joinedImprovements = true
			}
		}
	}

	// For sales comparables, join real property transfers to get the latest sales transaction
	if isSales {
		joins = append(joins, `
            LEFT JOIN (
                SELECT DISTINCT ON (rtpp.parcel_id)
                    rtpp.parcel_id,
                    rpt.transfer_timestamp,
                    rpt.transfer_amount::numeric(19,4)::text AS sale_price
                FROM real_property_transfer_party_parcels rtpp
                JOIN real_property_transfers rpt ON rtpp.real_property_transfer_id = rpt.real_property_transfer_id
                WHERE ($3::timestamptz IS NULL OR rpt.transfer_timestamp >= $3::timestamptz)
                  AND ($4::timestamptz IS NULL OR rpt.transfer_timestamp <= $4::timestamptz)
                ORDER BY rtpp.parcel_id, rpt.transfer_timestamp DESC
            ) sales_agg ON p.parcel_id = sales_agg.parcel_id`)
		selectFields = append(selectFields, "sales_agg.transfer_timestamp", "sales_agg.sale_price")
		scanDest = append(scanDest, &saleTime, &salePrice)
	}

	// SQL Query construction: filters either strictly by selected public IDs or geographically by ST_Intersects
	var filterCond string
	if len(candidateIDs) > 0 {
		filterCond = "($1::uuid[] IS NOT NULL AND p.public_id = ANY($1::uuid[]) AND ($2::text IS NULL OR TRUE))"
	} else {
		filterCond = "($1::uuid[] IS NULL AND $2::text IS NOT NULL AND ST_Intersects(pg.geom_web, ST_GeomFromText($2, 4326)))"
	}

	query := fmt.Sprintf(`
        SELECT 
            %s
        FROM parcels p
        %s
        WHERE NOT p.is_voided
          AND %s
    `, strings.Join(selectFields, ",\n"), strings.Join(joins, "\n"), filterCond)

	s.logger.Debug("performing dynamic comps query", slog.String("sql", query))

	var candidateIDsArg *[]string
	if len(candidateIDs) > 0 {
		candidateIDsArg = &candidateIDs
	}

	// Build exact query arguments list to prevent prepared statement parameter count mismatches (SQLSTATE 08P01)
	var queryArgs = []any{candidateIDsArg, wktPolygon}
	if isSales {
		queryArgs = append(queryArgs, startTime, endTime)
	}

	rows, err := s.db.Query(ctx, query, queryArgs...)
	if err != nil {
		s.logger.Error("failed to query parcels for comps", slog.Any("error", err))
		return nil, fmt.Errorf("failed to retrieve comp parcel data: %w", err)
	}
	defer rows.Close()

	parcels := make(map[string]*CompParcelInfo)
	for rows.Next() {
		err := rows.Scan(scanDest...)
		if err != nil {
			s.logger.Error("failed to scan comp parcel row", slog.Any("error", err))
			return nil, fmt.Errorf("data unmarshaling error in comps query: %w", err)
		}

		// Unit conversions: Convert metric database columns to imperial for the Protobuf API contract
		var landAreaSqFt, frontageFt, depthFt *float64
		if landAreaSqM != nil {
			// Convert square meters to square feet (1 sq m = 10.7639 sq ft)
			val := *landAreaSqM * 10.7639
			landAreaSqFt = &val
		}
		if frontageM != nil {
			// Convert meters to feet (1 m = 3.28084 ft)
			val := *frontageM * 3.28084
			frontageFt = &val
		}
		if depthM != nil {
			// Convert meters to feet (1 m = 3.28084 ft)
			val := *depthM * 3.28084
			depthFt = &val
		}
		// Convert total building/improvement area to square feet
		totalAreaSqFt := totalAreaSqM * 10.7639

		// Fallback rules for year built (use newest year built, fallback to oldest)
		var yearBuilt *int32
		if newestYearBuilt != nil {
			yearBuilt = newestYearBuilt
		} else {
			yearBuilt = oldestYearBuilt
		}

		// Fallback rules for effective year built (use newest, fallback to oldest)
		var effectiveYearBuilt *int32
		if newestEffectiveYearBuilt != nil {
			effectiveYearBuilt = newestEffectiveYearBuilt
		} else {
			effectiveYearBuilt = oldestEffectiveYearBuilt
		}

		// Prevent nil slice issues by returning empty slices instead of null in JSON response
		if zoningIDs == nil {
			zoningIDs = []string{}
		}
		if conditionIDs == nil {
			conditionIDs = []string{}
		}
		if improvementTypeIDs == nil {
			improvementTypeIDs = []string{}
		}

		addrID := ""
		if addressID != nil {
			addrID = *addressID
		}
		addrStr := ""
		if address != nil {
			addrStr = *address
		}

		parcels[parcelID] = &CompParcelInfo{
			ParcelID:            parcelID,
			AddressID:           addrID,
			FormattedAddress:    addrStr,
			LandAreaSqFt:        landAreaSqFt,
			FrontageFt:          frontageFt,
			DepthFt:             depthFt,
			LandUseID:           landUseID,
			ZoningIDs:           zoningIDs,
			ImprovementAreaSqFt: totalAreaSqFt,
			YearBuilt:           yearBuilt,
			EffectiveYearBuilt:  effectiveYearBuilt,
			Bedrooms:            totalBedrooms,
			Bathrooms:           totalBathrooms,
			Units:               totalUnits,
			ConditionIDs:        conditionIDs,
			ImprovementTypeIDs:  improvementTypeIDs,
			SaleTime:            saleTime,
			SalePrice:           salePrice,
			FeatureID:           featureID,
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error reading parcel rows: %w", err)
	}

	return parcels, nil
}

func getNumericalValue(p *CompParcelInfo, attr parcelsv1.ParcelAttribute) (*float64, bool) {
	switch attr {
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_LAND_AREA_SQ_FT:
		return p.LandAreaSqFt, true
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_FRONTAGE_FT:
		return p.FrontageFt, true
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_DEPTH_FT:
		return p.DepthFt, true
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_IMPROVEMENT_AREA_SQ_FT:
		val := p.ImprovementAreaSqFt
		return &val, true
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_IMPROVEMENT_YEAR_BUILT:
		if p.YearBuilt == nil {
			return nil, true
		}
		val := float64(*p.YearBuilt)
		return &val, true
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_IMPROVEMENT_EFFECTIVE_YEAR_BUILT:
		if p.EffectiveYearBuilt == nil {
			return nil, true
		}
		val := float64(*p.EffectiveYearBuilt)
		return &val, true
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_BEDROOMS:
		val := float64(p.Bedrooms)
		return &val, true
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_BATHROOMS:
		val := float64(p.Bathrooms)
		return &val, true
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_UNITS:
		val := float64(p.Units)
		return &val, true
	}
	return nil, false
}

func checkCategoricalMatch(cand *CompParcelInfo, attr parcelsv1.ParcelAttribute, tolerance []string) bool {
	switch attr {
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_LAND_USE_ID:
		candVal := ""
		if cand.LandUseID != nil {
			candVal = *cand.LandUseID
		}
		for _, t := range tolerance {
			if t == candVal {
				return true
			}
		}
		return false

	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_ZONING_ID:
		return sliceOverlap(cand.ZoningIDs, tolerance)

	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_CONDITION_ID:
		return sliceOverlap(cand.ConditionIDs, tolerance)

	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_IMPROVEMENT_TYPE_ID:
		return sliceOverlap(cand.ImprovementTypeIDs, tolerance)
	}
	return false
}

func sliceOverlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func getCategoricalValueString(p *CompParcelInfo, attr parcelsv1.ParcelAttribute) *string {
	switch attr {
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_LAND_USE_ID:
		return p.LandUseID
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_ZONING_ID:
		if len(p.ZoningIDs) == 0 {
			return nil
		}
		val := strings.Join(p.ZoningIDs, ",")
		return &val
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_CONDITION_ID:
		if len(p.ConditionIDs) == 0 {
			return nil
		}
		val := strings.Join(p.ConditionIDs, ",")
		return &val
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_IMPROVEMENT_TYPE_ID:
		if len(p.ImprovementTypeIDs) == 0 {
			return nil
		}
		val := strings.Join(p.ImprovementTypeIDs, ",")
		return &val
	}
	return nil
}

func (s *APIServer) GetEquityComparables(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetEquityComparablesRequest],
) (*connect.Response[parcelsv1.GetEquityComparablesResponse], error) {

	s.logger.Debug("received GetEquityComparables request",
		slog.Any("selected_parcel_ids", req.Msg.SelectedParcelIds),
		slog.String("wkt_polygon", req.Msg.WktPolygon))

	if len(req.Msg.SelectedParcelIds) == 0 && req.Msg.GetWktPolygon() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("either selected_parcel_ids or wkt_polygon must be provided"))
	}

	var candidateIDs []string
	var polygonArg *string

	if len(req.Msg.SelectedParcelIds) > 0 {
		candidateIDs = req.Msg.SelectedParcelIds
	} else {
		if p := req.Msg.GetWktPolygon(); p != "" {
			polygonArg = &p
		}
	}

	parcelsMap, err := s.fetchParcelsForComps(ctx, candidateIDs, polygonArg, nil, nil, req.Msg.Criteria, false)
	if err != nil {
		s.logger.Error("failed to fetch parcels for equity comps", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("database query error"))
	}

	resParcels := make(map[string]*parcelsv1.EquityComparableParcel)

	for id, cand := range parcelsMap {
		matched := true
		var attrs []*parcelsv1.ComparableAttribute

		for _, crit := range req.Msg.Criteria {
			if crit == nil {
				continue
			}

			attr := crit.GetAttribute()
			numVal, isNum := getNumericalValue(cand, attr)
			if isNum {
				if numVal == nil {
					matched = false
					break
				}
				if crit.MinNumericalTolerance != nil {
					if *numVal < *crit.MinNumericalTolerance {
						matched = false
						break
					}
				}
				if crit.MaxNumericalTolerance != nil {
					if *numVal > *crit.MaxNumericalTolerance {
						matched = false
						break
					}
				}
				attrs = append(attrs, &parcelsv1.ComparableAttribute{
					Attribute:      attr,
					NumericalValue: numVal,
				})
			} else {
				if len(crit.GetCategoricalTolerance()) > 0 {
					if !checkCategoricalMatch(cand, attr, crit.GetCategoricalTolerance()) {
						matched = false
						break
					}
				}
				catStr := getCategoricalValueString(cand, attr)
				attrs = append(attrs, &parcelsv1.ComparableAttribute{
					Attribute:        attr,
					CategoricalValue: catStr,
				})
			}
		}

		if matched {
			resParcels[id] = &parcelsv1.EquityComparableParcel{
				ParcelId:         cand.ParcelID,
				FeatureId:        cand.FeatureID,
				AddressId:        cand.AddressID,
				FormattedAddress: cand.FormattedAddress,
				Attributes:       attrs,
			}
		}
	}

	return connect.NewResponse(&parcelsv1.GetEquityComparablesResponse{
		Parcels: resParcels,
	}), nil
}

func (s *APIServer) GetSalesComparables(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetSalesComparablesRequest],
) (*connect.Response[parcelsv1.GetSalesComparablesResponse], error) {

	s.logger.Debug("received GetSalesComparables request",
		slog.Any("selected_parcel_ids", req.Msg.SelectedParcelIds),
		slog.String("wkt_polygon", req.Msg.WktPolygon))

	if len(req.Msg.SelectedParcelIds) == 0 && req.Msg.GetWktPolygon() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("either selected_parcel_ids or wkt_polygon must be provided"))
	}

	var candidateIDs []string
	var polygonArg *string

	if len(req.Msg.SelectedParcelIds) > 0 {
		candidateIDs = req.Msg.SelectedParcelIds
	} else {
		if p := req.Msg.GetWktPolygon(); p != "" {
			polygonArg = &p
		}
	}

	var startTime, endTime *time.Time
	if tr := req.Msg.GetTimeRange(); tr != nil {
		if st := tr.GetStartTime(); st != nil {
			t := st.AsTime()
			startTime = &t
		}
		if et := tr.GetEndTime(); et != nil {
			t := et.AsTime()
			endTime = &t
		}
	}

	parcelsMap, err := s.fetchParcelsForComps(ctx, candidateIDs, polygonArg, startTime, endTime, req.Msg.Criteria, true)
	if err != nil {
		s.logger.Error("failed to fetch parcels for sales comps", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("database query error"))
	}

	resParcels := make(map[string]*parcelsv1.SaleComparableParcel)

	for id, cand := range parcelsMap {
		if cand.SaleTime == nil || cand.SalePrice == nil {
			continue
		}

		matched := true
		var attrs []*parcelsv1.ComparableAttribute

		for _, crit := range req.Msg.Criteria {
			if crit == nil {
				continue
			}

			attr := crit.GetAttribute()
			numVal, isNum := getNumericalValue(cand, attr)
			if isNum {
				if numVal == nil {
					matched = false
					break
				}
				if crit.MinNumericalTolerance != nil {
					if *numVal < *crit.MinNumericalTolerance {
						matched = false
						break
					}
				}
				if crit.MaxNumericalTolerance != nil {
					if *numVal > *crit.MaxNumericalTolerance {
						matched = false
						break
					}
				}
				attrs = append(attrs, &parcelsv1.ComparableAttribute{
					Attribute:      attr,
					NumericalValue: numVal,
				})
			} else {
				if len(crit.GetCategoricalTolerance()) > 0 {
					if !checkCategoricalMatch(cand, attr, crit.GetCategoricalTolerance()) {
						matched = false
						break
					}
				}
				catStr := getCategoricalValueString(cand, attr)
				attrs = append(attrs, &parcelsv1.ComparableAttribute{
					Attribute:        attr,
					CategoricalValue: catStr,
				})
			}
		}

		if matched {
			resParcels[id] = &parcelsv1.SaleComparableParcel{
				ParcelId:         cand.ParcelID,
				FeatureId:        cand.FeatureID,
				AddressId:        cand.AddressID,
				FormattedAddress: cand.FormattedAddress,
				SaleTime:         timestamppb.New(*cand.SaleTime),
				SalePrice:        *cand.SalePrice,
				Attributes:       attrs,
			}
		}
	}

	return connect.NewResponse(&parcelsv1.GetSalesComparablesResponse{
		Parcels: resParcels,
	}), nil
}

func (s *APIServer) GetParcelByFeatureId(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetParcelByFeatureIdRequest],
) (*connect.Response[parcelsv1.GetParcelByFeatureIdResponse], error) {

	s.logger.Debug("received GetParcelByFeatureId request", slog.Int64("featureId", req.Msg.FeatureId),
		slog.Any("neighborhoodDefinitionId", req.Msg.NeighborhoodDefinitionId),
		slog.Any("valuationId", req.Msg.ValuationId))

	var neighborhoodDefIDArg *string
	if defID := req.Msg.GetNeighborhoodDefinitionId(); defID != "" {
		neighborhoodDefIDArg = &defID
	}

	var valuationIDArg *string
	if valID := req.Msg.GetValuationId(); valID != "" {
		valuationIDArg = &valID
	}

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

			pv_agg.market_land_value,
			pv_agg.assessed_land_value,

            pa.properties::text,
            pg.feature_id
        FROM parcels p
        JOIN parcel_geometry pg ON p.parcel_id = pg.parcel_id
        LEFT JOIN parcel_attributes pa ON p.parcel_id = pa.parcel_id 
        LEFT JOIN addresses a ON pa.address_id = a.address_id
        LEFT JOIN address_attributes aa ON a.address_id = aa.address_id
        LEFT JOIN land_uses lu ON pa.land_use_id = lu.land_use_id

        -- New Party / Fractional Ownership Aggregation
        LEFT JOIN LATERAL (
            SELECT 
                -- Aggregate all unique party IDs, ordered by who holds the largest share
                array_agg(pty.public_id::text ORDER BY pp.ownership_share DESC, pty.party_id ASC) AS party_ids,
                -- Grab the name and address of the party with the highest ownership share to act as the "Primary"
                (array_agg(pa_attr.name ORDER BY pp.ownership_share DESC, pty.party_id ASC))[1] AS primary_owner_name,
                (array_agg(p_aa.formatted_address ORDER BY pp.ownership_share DESC, pty.party_id ASC))[1] AS primary_owner_address
            FROM parcel_parties pp
            JOIN parties pty ON pp.party_id = pty.party_id
            LEFT JOIN party_attributes pa_attr ON pty.party_id = pa_attr.party_id
            LEFT JOIN address_attributes p_aa ON pa_attr.address_id = p_aa.address_id
            WHERE pp.parcel_id = p.parcel_id
        ) parties_agg ON TRUE

        -- Neighborhood Definition Joins
        LEFT JOIN neighborhood_definitions nd 
            ON nd.public_id = $2::uuid
        LEFT JOIN parcel_neighborhood_definitions pnd 
            ON p.parcel_id = pnd.parcel_id 
            AND pnd.neighborhood_definition_id = nd.neighborhood_definition_id
        LEFT JOIN neighborhoods n 
            ON pnd.neighborhood_id = n.neighborhood_id

        -- Aggregated Affordances Subquery
        LEFT JOIN LATERAL (
            SELECT 
                array_remove(array_agg(DISTINCT z.public_id::text), NULL) AS zoning_public_ids
            FROM parcel_affordances pa
			LEFT JOIN zoning z ON pa.zoning_id = z.zoning_id
            WHERE pa.parcel_id = p.parcel_id
        ) aff ON TRUE

        -- Aggregated Parcel Valuations Subquery
        LEFT JOIN LATERAL (
            SELECT 
                pv.market_value::numeric(19,4)::text AS market_land_value,
                pv.assessed_value::numeric(19,4)::text AS assessed_land_value
            FROM parcel_valuations pv
            JOIN valuations v ON pv.valuation_id = v.valuation_id
            WHERE pv.parcel_id = p.parcel_id
              AND $3::uuid IS NOT NULL AND v.public_id = $3::uuid
        ) pv_agg ON TRUE

        WHERE pg.feature_id = $1 
          AND pg.legal_valid_range @> NOW()
          AND NOT p.is_voided
        LIMIT 1
    `

	rows, err := s.db.Query(ctx, query, req.Msg.FeatureId, neighborhoodDefIDArg, valuationIDArg)
	if err != nil {
		s.logger.Error("failed to query parcel by feature ID", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to retrieve parcel data"))
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("parcel not found for feature ID %d", req.Msg.FeatureId))
	}

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

		zoningIDs         []string
		marketLandValue   *string
		assessedLandValue *string

		properties *string
		featureID  int64
	)

	err = rows.Scan(
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

		&marketLandValue,
		&assessedLandValue,

		&properties,
		&featureID,
	)

	if err != nil {
		s.logger.Error("failed to scan parcel row by feature ID", "error", err, "featureId", req.Msg.FeatureId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("data unmarshaling error"))
	}

	var landAreaSqFt, frontageFt, depthFt *float64

	if landAreaSqM != nil {
		val := *landAreaSqM * 10.7639
		landAreaSqFt = &val
	}

	if frontageM != nil {
		val := *frontageM * 3.28084
		frontageFt = &val
	}

	if depthM != nil {
		val := *depthM * 3.28084
		depthFt = &val
	}

	if zoningIDs == nil {
		zoningIDs = []string{}
	}

	parcel := &parcelsv1.SimpleParcel{
		ParcelId:            parcelID,
		FeatureId:           featureID,
		FormattedAddress:    address,
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
		MarketLandValue:     marketLandValue,
		AssessedLandValue:   assessedLandValue,
		Properties:          properties,
	}

	if err := rows.Err(); err != nil {
		s.logger.Error("error iterating parcel row by feature ID", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to process parcel data"))
	}

	return connect.NewResponse(&parcelsv1.GetParcelByFeatureIdResponse{
		Parcel: parcel,
	}), nil
}

func (s *APIServer) GetParcelIdsByFeatureId(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetParcelIdsByFeatureIdRequest],
) (*connect.Response[parcelsv1.GetParcelIdsByFeatureIdResponse], error) {

	s.logger.Debug("received GetParcelIdsByFeatureId request", slog.Any("featureIds", req.Msg.FeatureIds))

	featureIDs := req.Msg.FeatureIds
	if len(featureIDs) == 0 {
		return connect.NewResponse(&parcelsv1.GetParcelIdsByFeatureIdResponse{
			ParcelIds: make(map[int64]string),
		}), nil
	}

	query := `
		SELECT pg.feature_id, p.public_id::text
		FROM parcel_geometry pg
		JOIN parcels p ON pg.parcel_id = p.parcel_id
		WHERE pg.feature_id = ANY($1::bigint[]) AND NOT p.is_voided
	`

	rows, err := s.db.Query(ctx, query, featureIDs)
	if err != nil {
		s.logger.Error("failed to query parcel IDs by feature ID", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to retrieve parcel IDs"))
	}
	defer rows.Close()

	parcelIDsMap := make(map[int64]string)
	for rows.Next() {
		var featureID int64
		var parcelUUID string
		if err := rows.Scan(&featureID, &parcelUUID); err != nil {
			s.logger.Error("failed to scan parcel ID row", slog.Any("error", err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("data unmarshaling error"))
		}
		parcelIDsMap[featureID] = parcelUUID
	}

	if err := rows.Err(); err != nil {
		s.logger.Error("error iterating parcel ID rows", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to process parcel ID stream"))
	}

	return connect.NewResponse(&parcelsv1.GetParcelIdsByFeatureIdResponse{
		ParcelIds: parcelIDsMap,
	}), nil
}

func (s *APIServer) GetEstimatedParcelsExtentWGS84(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetEstimatedParcelsExtentWGS84Request],
) (*connect.Response[parcelsv1.GetEstimatedParcelsExtentWGS84Response], error) {

	s.logger.Debug("received GetEstimatedParcelsExtentWGS84 request")

	var minX, minY, maxX, maxY float64
	query := `
		SELECT 
			COALESCE(ST_XMin(ext), 0.0), 
			COALESCE(ST_YMin(ext), 0.0), 
			COALESCE(ST_XMax(ext), 0.0), 
			COALESCE(ST_YMax(ext), 0.0) 
		FROM (
			SELECT ST_EstimatedExtent('parcel_geometry', 'geom_web') AS ext
		) AS sub
	`
	err := s.db.QueryRow(ctx, query).Scan(&minX, &minY, &maxX, &maxY)
	if err != nil {
		s.logger.Warn("ST_EstimatedExtent failed, falling back to ST_Extent", slog.Any("error", err))
		fallbackQuery := `
			SELECT 
				COALESCE(ST_XMin(ST_Extent(geom_web)), 0.0), 
				COALESCE(ST_YMin(ST_Extent(geom_web)), 0.0), 
				COALESCE(ST_XMax(ST_Extent(geom_web)), 0.0), 
				COALESCE(ST_YMax(ST_Extent(geom_web)), 0.0) 
			FROM parcel_geometry
		`
		err = s.db.QueryRow(ctx, fallbackQuery).Scan(&minX, &minY, &maxX, &maxY)
		if err != nil {
			s.logger.Error("fallback ST_Extent query failed", slog.Any("error", err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to retrieve estimated parcels extent"))
		}
	}

	res := &parcelsv1.GetEstimatedParcelsExtentWGS84Response{
		MinX: minX,
		MinY: minY,
		MaxX: maxX,
		MaxY: maxY,
	}

	return connect.NewResponse(res), nil
}
