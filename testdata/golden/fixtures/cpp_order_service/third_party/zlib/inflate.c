// Third-party zlib implementation stub (must be skipped by Layer 1 noise filter)
#include "zlib.h"
int inflateInit(struct z_stream_s* strm) { return strm ? 0 : -1; }
