//! Runtime call context.
use std::{any::Any, sync::Arc};

use io_context::Context as IoContext;

use super::tags::{Tag, Tags};
use crate::common::roothash::{Header, RoothashMessage};

struct NoRuntimeContext;

/// Transaction context.
pub struct Context<'a> {
    /// I/O context.
    pub io_ctx: Arc<IoContext>,
    /// The block header accompanying this transaction.
    pub header: &'a Header,
    /// Runtime-specific context.
    pub runtime: Box<dyn Any>,

    /// Flag indicating whether to only perform transaction check rather than
    /// running the transaction.
    pub check_only: bool,

    /// List of emitted tags for each transaction.
    tags: Vec<Tags>,

    /// List of roothash messages emitted.
    roothash_messages: Vec<RoothashMessage>,
}

impl<'a> Context<'a> {
    /// Construct new transaction context.
    pub fn new(io_ctx: Arc<IoContext>, header: &'a Header, check_only: bool) -> Self {
        Self {
            io_ctx,
            header,
            runtime: Box::new(NoRuntimeContext),
            check_only,
            tags: Vec::new(),
            roothash_messages: Vec::new(),
        }
    }

    /// Start a new transaction.
    pub(crate) fn start_transaction(&mut self) {
        self.tags.push(Tags::new());
    }

    /// Close the context and return the emitted tags and sent roothash messages.
    pub(crate) fn close(self) -> (Vec<Tags>, Vec<RoothashMessage>) {
        (self.tags, self.roothash_messages)
    }

    /// Emit a runtime-specific indexable tag refering to the specific
    /// transaction which is being processed.
    ///
    /// If multiple tags with the same key are emitted for a transaction, only
    /// the last one will be indexed.
    ///
    /// # Panics
    ///
    /// Calling this method outside of a transaction will panic.
    ///
    pub fn emit_txn_tag<K, V>(&mut self, key: K, value: V)
    where
        K: AsRef<[u8]>,
        V: AsRef<[u8]>,
    {
        assert!(
            !self.tags.is_empty(),
            "must only be called inside a transaction"
        );

        self.tags
            .last_mut()
            .expect("tags is not empty")
            .push(Tag::new(key.as_ref().to_vec(), value.as_ref().to_vec()))
    }

    /// Send a roothash message as part of the block that contains this transaction.
    /// See RFC 0065 for information on roothash messages.
    pub fn send_roothash_message(&mut self, roothash_message: RoothashMessage) {
        self.roothash_messages.push(roothash_message);
    }
}
