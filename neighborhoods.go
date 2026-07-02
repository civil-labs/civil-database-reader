package main

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	meshneighborhoodsv1 "github.com/civil-labs/civil-api-go/civil/mesh/neighborhoods/v1"
)

func (s *APIServer) GetNeighborhoodDefinitions(
	ctx context.Context,
	req *connect.Request[meshneighborhoodsv1.GetNeighborhoodDefinitionsRequest],
) (*connect.Response[meshneighborhoodsv1.GetNeighborhoodDefinitionsResponse], error) {
	s.logger.Debug("received GetNeighborhoodDefinitions request")

	rows, err := s.db.Query(ctx, `
		SELECT 
			public_id::text, 
			name
		FROM public.neighborhood_definitions
	`)
	if err != nil {
		s.logger.Error("failed to query neighborhood definitions", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("database query error"))
	}
	defer rows.Close()

	defsMap := make(map[string]*meshneighborhoodsv1.Neighborhood)
	for rows.Next() {
		var publicID, name string
		err := rows.Scan(
			&publicID,
			&name,
		)
		if err != nil {
			s.logger.Error("failed to scan neighborhood definition row", slog.Any("error", err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("data scanning error"))
		}

		defsMap[publicID] = &meshneighborhoodsv1.Neighborhood{
			Id:   publicID,
			Name: name,
		}
	}

	if err := rows.Err(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("error reading database rows"))
	}

	return connect.NewResponse(&meshneighborhoodsv1.GetNeighborhoodDefinitionsResponse{
		Neighborhoods: defsMap,
	}), nil
}

func (s *APIServer) GetNeighborhoods(
	ctx context.Context,
	req *connect.Request[meshneighborhoodsv1.GetNeighborhoodsRequest],
) (*connect.Response[meshneighborhoodsv1.GetNeighborhoodsResponse], error) {
	s.logger.Debug("received GetNeighborhoods request")

	rows, err := s.db.Query(ctx, `
		SELECT 
			public_id::text, 
			name
		FROM public.neighborhoods
	`)
	if err != nil {
		s.logger.Error("failed to query neighborhoods", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("database query error"))
	}
	defer rows.Close()

	neighborhoodsMap := make(map[string]*meshneighborhoodsv1.Neighborhood)
	for rows.Next() {
		var publicID, name string
		err := rows.Scan(
			&publicID,
			&name,
		)
		if err != nil {
			s.logger.Error("failed to scan neighborhood row", slog.Any("error", err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("data scanning error"))
		}

		neighborhoodsMap[publicID] = &meshneighborhoodsv1.Neighborhood{
			Id:   publicID,
			Name: name,
		}
	}

	if err := rows.Err(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("error reading database rows"))
	}

	return connect.NewResponse(&meshneighborhoodsv1.GetNeighborhoodsResponse{
		Neighborhoods: neighborhoodsMap,
	}), nil
}
