# Separate conversation reset from memory deletion

A conversation reset clears the current dialog and its summary, and prevents earlier dialogs and temporary notes from automatically entering the new conversation. Keep intentionally saved facts and archived dialogs; access to the archive requires an explicit request. Clearing only the current transcript would let automatic retrieval restore the context the user intended to clear, while deleting all memory would also remove information the user intended to retain.
