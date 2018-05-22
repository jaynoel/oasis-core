use std::convert::{Into, TryFrom};
use std::sync::Arc;

use ekiden_common::bytes::B256;
use ekiden_common::error::Result;
use ekiden_common::futures::{future, Future, Stream};
use ekiden_common::signature::{Signature, Signed};
use ekiden_consensus_api as api;
use grpcio::{RpcContext, RpcStatus, ServerStreamingSink, UnarySink, WriteFlags};
use grpcio::RpcStatusCode::{Internal, InvalidArgument};

use super::backend::{ConsensusBackend, Event};
use block::Block;
use commitment::{Commitment, Reveal};
use header::Header;

#[derive(Clone)]
pub struct ConsensusService {
    inner: Arc<ConsensusBackend>,
}

impl ConsensusService {
    pub fn new(backend: Arc<ConsensusBackend>) -> Self {
        Self { inner: backend }
    }
}

macro_rules! invalid {
    ($sink:ident,$code:ident,$e:expr) => {
        $sink.fail(RpcStatus::new(
            $code,
            Some($e.description().to_owned()),
        ))
    }
}

impl api::Consensus for ConsensusService {
    fn get_latest_block(
        &self,
        ctx: RpcContext,
        req: api::LatestBlockRequest,
        sink: UnarySink<api::LatestBlockResponse>,
    ) {
        let f = move || -> Result<_> {
            Ok(self.inner
                .get_latest_block(B256::try_from(req.get_contract_id())?))
        };
        let f = match f() {
            Ok(f) => f.then(|response| match response {
                Ok(block) => {
                    let mut pb_response = api::LatestBlockResponse::new();
                    pb_response.set_block(block.into());

                    Ok(pb_response)
                }
                Err(error) => Err(error),
            }),
            Err(error) => {
                ctx.spawn(invalid!(sink, InvalidArgument, error).map_err(|_error| ()));
                return;
            }
        };
        ctx.spawn(f.then(move |response| match response {
            Ok(response) => sink.success(response),
            Err(error) => invalid!(sink, Internal, error),
        }).map_err(|_error| ()));
    }

    fn get_blocks(
        &self,
        ctx: RpcContext,
        req: api::BlockRequest,
        sink: ServerStreamingSink<api::BlockResponse>,
    ) {
        let f = move || -> Result<_> {
            Ok(self.inner
                .get_blocks(B256::try_from(req.get_contract_id())?))
        };
        let f = match f() {
            Ok(f) => f.map(|response| -> (api::BlockResponse, WriteFlags) {
                let mut pb_response = api::BlockResponse::new();
                pb_response.set_block(response.into());

                (pb_response, WriteFlags::default())
            }),
            Err(error) => {
                ctx.spawn(invalid!(sink, InvalidArgument, error).map_err(|_error| ()));
                return;
            }
        };
        ctx.spawn(f.forward(sink).then(|_f| future::ok(())));
    }

    fn get_events(
        &self,
        ctx: RpcContext,
        req: api::EventRequest,
        sink: ServerStreamingSink<api::EventResponse>,
    ) {
        let f = move || -> Result<_> {
            Ok(self.inner
                .get_events(B256::try_from(req.get_contract_id())?))
        };
        let f = match f() {
            Ok(f) => f.map(|response| -> (api::EventResponse, WriteFlags) {
                let mut pb_response = api::EventResponse::new();
                match response {
                    Event::CommitmentsReceived => {
                        pb_response.set_event(api::EventResponse_Event::COMMITMENTSRECEIVED)
                    }
                    Event::RoundFailed(_) => {
                        pb_response.set_event(api::EventResponse_Event::ROUNDFAILED)
                    }
                };

                (pb_response, WriteFlags::default())
            }),
            Err(error) => {
                ctx.spawn(invalid!(sink, InvalidArgument, error).map_err(|_error| ()));
                return;
            }
        };
        ctx.spawn(f.forward(sink).then(|_f| future::ok(())));
    }

    fn commit(
        &self,
        ctx: RpcContext,
        req: api::CommitRequest,
        sink: UnarySink<api::CommitResponse>,
    ) {
        let f = move || -> Result<_> {
            let contract_id = B256::try_from(req.get_contract_id())?;
            let commitment = Commitment::try_from(req.get_commitment().clone())?;
            Ok(self.inner.commit(contract_id, commitment))
        };
        let f = match f() {
            Ok(f) => f.then(|res| match res {
                Ok(()) => Ok(api::CommitResponse::new()),
                Err(e) => Err(e),
            }),
            Err(error) => {
                ctx.spawn(invalid!(sink, InvalidArgument, error).map_err(|_error| ()));
                return;
            }
        };
        ctx.spawn(f.then(move |response| match response {
            Ok(response) => sink.success(response),
            Err(error) => invalid!(sink, Internal, error),
        }).map_err(|_error| ()));
    }

    fn reveal(
        &self,
        ctx: RpcContext,
        req: api::RevealRequest,
        sink: UnarySink<api::RevealResponse>,
    ) {
        let f = move || -> Result<_> {
            let contract_id = B256::try_from(req.get_contract_id())?;
            let header = Header::try_from(req.get_header().clone())?;
            let nonce = B256::from(req.get_nonce());
            let signature = Signature::try_from(req.get_signature().clone())?;
            Ok(self.inner.reveal(
                contract_id,
                Reveal {
                    value: header,
                    nonce: nonce,
                    signature: signature,
                },
            ))
        };
        let f = match f() {
            Ok(f) => f.then(|response| match response {
                Ok(()) => Ok(api::RevealResponse::new()),
                Err(e) => Err(e),
            }),
            Err(error) => {
                ctx.spawn(invalid!(sink, InvalidArgument, error).map_err(|_error| ()));
                return;
            }
        };
        ctx.spawn(f.then(move |response| match response {
            Ok(response) => sink.success(response),
            Err(error) => invalid!(sink, Internal, error),
        }).map_err(|_error| ()));
    }

    fn submit(
        &self,
        ctx: RpcContext,
        req: api::SubmitRequest,
        sink: UnarySink<api::SubmitResponse>,
    ) {
        let f = move || -> Result<_> {
            let contract_id = B256::try_from(req.get_contract_id())?;
            let block = Block::try_from(req.get_block().clone())?;
            let signature = Signature::try_from(req.get_signature().clone())?;
            Ok(self.inner
                .submit(contract_id, Signed::from_parts(block, signature)))
        };
        let f = match f() {
            Ok(f) => f.then(|res| match res {
                Ok(()) => Ok(api::SubmitResponse::new()),
                Err(error) => Err(error),
            }),
            Err(error) => {
                ctx.spawn(invalid!(sink, InvalidArgument, error).map_err(|_error| ()));
                return;
            }
        };
        ctx.spawn(f.then(move |response| match response {
            Ok(response) => sink.success(response),
            Err(error) => invalid!(sink, Internal, error),
        }).map_err(|_error| ()));
    }
}