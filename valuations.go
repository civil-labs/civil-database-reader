package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	meshvaluationsv1 "github.com/civil-labs/civil-api-go/civil/mesh/valuations/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *APIServer) GetValuations(
	ctx context.Context,
	req *connect.Request[meshvaluationsv1.GetValuationsRequest],
) (*connect.Response[meshvaluationsv1.GetValuationsResponse], error) {
	s.logger.Debug("received GetValuations request")

	rows, err := s.db.Query(ctx, `
		SELECT 
			public_id::text, 
			valuation_date
		FROM public.valuations
	`)
	if err != nil {
		s.logger.Error("failed to query valuations", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("database query error"))
	}
	defer rows.Close()

	valuationsMap := make(map[string]*meshvaluationsv1.Valuation)
	for rows.Next() {
		var (
			publicID string
			valDate  time.Time
		)
		err := rows.Scan(
			&publicID,
			&valDate,
		)
		if err != nil {
			s.logger.Error("failed to scan valuation row", slog.Any("error", err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("data scanning error"))
		}

		valuationsMap[publicID] = &meshvaluationsv1.Valuation{
			Id:                 publicID,
			ValuationTimestamp: timestamppb.New(valDate),
		}
	}

	if err := rows.Err(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("error reading database rows"))
	}

	return connect.NewResponse(&meshvaluationsv1.GetValuationsResponse{
		Valuations: valuationsMap,
	}), nil
}
