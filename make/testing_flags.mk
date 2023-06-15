TEST_FLAGS =
DEV_TAGS = dev
COVER_PKG = $$(go list -deps -tags="$(DEV_TAGS)" ./... | grep '$(PKG)' | grep -v lnrpc)
GOLIST := go list -tags="$(DEV_TAGS)" -deps $(PKG)/... | grep '$(PKG)'| grep -v '/vendor/'

# If specific package is being unit tested, construct the full name of the
# subpackage.
ifneq ($(pkg),)
UNITPKG := $(PKG)/$(pkg)
UNIT_TARGETED = yes
COVER_PKG = $(PKG)/$(pkg)
endif

# If a specific unit test case is being target, construct test.run filter.
ifneq ($(case),)
TEST_FLAGS += -test.run=$(case)
UNIT_TARGETED = yes
endif

# If a timeout was requested, construct initialize the proper flag for the go
# test command. If not, we set 20m (up from the default 10m).
ifneq ($(timeout),)
TEST_FLAGS += -test.timeout=$(timeout)
else
TEST_FLAGS += -test.timeout=20m
endif

# If we are targetting postgres make sure our tests have the tags.
ifeq ($(dbbackend),postgres)
DEV_TAGS += test_db_postgres
endif

# Add any additional tags to the dev tags list.
ifneq ($(tags),)
DEV_TAGS += ${tags}
endif

# UNIT_TARGTED is undefined iff a specific package and/or unit test case is
# not being targeted.
UNIT_TARGETED ?= no

# If a specific package/test case was requested, run the unit test for the
# targeted case. Otherwise, default to running all tests.
ifeq ($(UNIT_TARGETED), yes)
UNIT := $(GOTEST) -tags="$(DEV_TAGS)" $(TEST_FLAGS) $(UNITPKG)
UNIT_RACE := $(GOTEST) -tags="$(DEV_TAGS)" $(TEST_FLAGS) -race $(UNITPKG)
endif

ifeq ($(UNIT_TARGETED), no)
UNIT := $(GOLIST) | $(XARGS) env $(GOTEST) -tags="$(DEV_TAGS)" $(TEST_FLAGS)
UNIT_RACE := $(GOLIST) | $(XARGS) env $(GOTEST) -tags="$(DEV_TAGS)" -race $(TEST_FLAGS)
endif
